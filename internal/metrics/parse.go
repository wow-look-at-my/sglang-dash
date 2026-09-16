// Package metrics parses the Prometheus text exposition format and keeps a
// bounded history of every series it has seen.
package metrics

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Sample is a single scraped series value.
type Sample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
	Help   string            `json:"help,omitempty"`
	Type   string            `json:"type,omitempty"`
}

// Key is the series identity: metric name plus its sorted label set.
func (s Sample) Key() string {
	if len(s.Labels) == 0 {
		return s.Name
	}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(s.Name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Labels[k])
	}
	b.WriteByte('}')
	return b.String()
}

// Parse reads the Prometheus text exposition format.
func Parse(r io.Reader) ([]Sample, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	help := map[string]string{}
	typ := map[string]string{}
	var out []Sample
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#") {
			fields := strings.Fields(text)
			if len(fields) >= 4 {
				switch fields[1] {
				case "HELP":
					help[fields[2]] = strings.Join(fields[3:], " ")
				case "TYPE":
					typ[fields[2]] = fields[3]
				}
			}
			continue
		}
		s, err := parseSample(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		// HELP and TYPE are declared per family, so a member looks its own up there.
		base := s.Name
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			if strings.HasSuffix(base, suffix) {
				base = strings.TrimSuffix(base, suffix)
				break
			}
		}
		s.Help = firstNonEmpty(help[s.Name], help[base])
		s.Type = firstNonEmpty(typ[s.Name], typ[base])
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseSample(text string) (Sample, error) {
	s := Sample{}
	open := strings.IndexByte(text, '{')
	rest := text
	if open >= 0 {
		closeIdx := strings.LastIndexByte(text, '}')
		if closeIdx < open {
			return s, fmt.Errorf("unbalanced label braces in %q", text)
		}
		s.Name = strings.TrimSpace(text[:open])
		labels, err := parseLabels(text[open+1 : closeIdx])
		if err != nil {
			return s, err
		}
		s.Labels = labels
		rest = strings.TrimSpace(text[closeIdx+1:])
	} else {
		sp := strings.IndexAny(text, " \t")
		if sp < 0 {
			return s, fmt.Errorf("no value in %q", text)
		}
		s.Name = text[:sp]
		rest = strings.TrimSpace(text[sp:])
	}
	// A trailing scrape timestamp is legal and is not the value.
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return s, fmt.Errorf("no value for %q", s.Name)
	}
	v, err := parseValue(fields[0])
	if err != nil {
		return s, fmt.Errorf("value for %q: %w", s.Name, err)
	}
	s.Value = v
	return s, nil
}

func parseValue(tok string) (float64, error) {
	switch tok {
	case "+Inf", "Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(tok, 64)
}

func parseLabels(body string) (map[string]string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil
	}
	out := map[string]string{}
	i := 0
	for i < len(body) {
		for i < len(body) && (body[i] == ' ' || body[i] == ',') {
			i++
		}
		if i >= len(body) {
			break
		}
		eq := strings.IndexByte(body[i:], '=')
		if eq < 0 {
			return nil, fmt.Errorf("label without '=' in %q", body)
		}
		name := strings.TrimSpace(body[i : i+eq])
		i += eq + 1
		if i >= len(body) || body[i] != '"' {
			return nil, fmt.Errorf("label %q value is not quoted", name)
		}
		i++
		var val strings.Builder
		for i < len(body) {
			c := body[i]
			if c == '\\' && i+1 < len(body) {
				i++
				switch body[i] {
				case 'n':
					val.WriteByte('\n')
				case 't':
					val.WriteByte('\t')
				default:
					val.WriteByte(body[i])
				}
				i++
				continue
			}
			if c == '"' {
				i++
				break
			}
			val.WriteByte(c)
			i++
		}
		out[name] = val.String()
	}
	return out, nil
}
