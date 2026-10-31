package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Secret is a credential found in some data. The value itself is never
// stored: only its kind, a masked preview and a hash to recognise it again.
type Secret struct {
	Kind   string `json:"kind"`
	Masked string `json:"masked"`
	Hash   string `json:"hash"` // first 16 hex of SHA-256(value)
}

// Sent is one piece of data that left the computer: a file an AI agent read
// (and so sent to its model), a command's output, a request's body.
type Sent struct {
	Source  string   `json:"source"`
	Bytes   int64    `json:"bytes"`
	Hash    string   `json:"hash,omitempty"`
	Secrets []Secret `json:"secrets,omitempty"`
}

type secretRule struct {
	kind string
	re   *regexp.Regexp
	grp  int // capture group holding the secret (0 = whole match)
}

var secretRules = []secretRule{
	{"Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`), 0},
	{"OpenAI API key", regexp.MustCompile(`sk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{20,}`), 0},
	{"GitHub token", regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})`), 0},
	{"AWS access key", regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`), 0},
	{"Stripe key", regexp.MustCompile(`(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`), 0},
	{"Razorpay key", regexp.MustCompile(`rzp_(?:live|test)_[A-Za-z0-9]{14,}`), 0},
	{"Google API key", regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`), 0},
	{"Slack token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`), 0},
	{"SendGrid key", regexp.MustCompile(`SG\.[A-Za-z0-9_\-]{22}\.[A-Za-z0-9_\-]{43}`), 0},
	{"Private key", regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----`), 0},
	{"JWT", regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), 0},
	{"Database URL with password", regexp.MustCompile(`(?i)(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^:\s/]+:([^@\s]{6,})@`), 1},
	{"Password or secret", regexp.MustCompile(`(?i)(?:password|passwd|pwd|secret|token|api[_\-]?key|access[_\-]?key|private[_\-]?key|client[_\-]?secret)["']?\s*[:=]\s*["']?([^\s"',;]{8,})`), 1},
}

var placeholder = regexp.MustCompile(`(?i)^(?:x+|\*+|\.+|<.*>|\$\{.*\}|\{\{.*\}\}|%.*%|your[_\-].*|changeme|example.*|placeholder|redacted|none|null|true|false|undefined|process\.env.*|os\.environ.*)$`)

// entropy is bits per character; random keys score high, words score low.
func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	for _, r := range s {
		freq[r]++
	}
	n := float64(len([]rune(s)))
	h := 0.0
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

func mask(v string) string {
	r := []rune(v)
	if len(r) <= 10 {
		return string(r[:min(2, len(r))]) + "…"
	}
	return string(r[:6]) + "…" + string(r[len(r)-4:])
}

func secretHash(v string) string {
	s := sha256.Sum256([]byte(v))
	return hex.EncodeToString(s[:])[:16]
}

// ScanSecrets finds credentials in data. Each distinct value is reported once.
func ScanSecrets(data []byte) []Secret {
	if len(data) > 8<<20 {
		data = data[:8<<20]
	}
	text := string(data)
	seen := map[string]bool{}
	var found []string // raw values of specific keys, so a generic match of the same key isn't counted twice
	var out []Secret
	for _, rule := range secretRules {
		for _, m := range rule.re.FindAllStringSubmatch(text, 50) {
			v := m[rule.grp]
			if rule.kind == "OpenAI API key" && strings.HasPrefix(v, "sk-ant-") {
				continue
			}
			if rule.grp > 0 {
				v = strings.Trim(v, `"'`)
				if placeholder.MatchString(v) || entropy(v) < 3.0 {
					continue
				}
				overlap := false
				for _, f := range found {
					if strings.Contains(v, f) || strings.Contains(f, v) {
						overlap = true
					}
				}
				if overlap {
					continue
				}
			}
			masked := mask(v)
			if rule.kind == "Private key" {
				masked = strings.Trim(m[0], "-")
				v = m[0] + secretHash(text) // one per file is enough
			}
			h := secretHash(v)
			if seen[h] {
				continue
			}
			seen[h] = true
			if rule.grp == 0 {
				found = append(found, v)
			}
			out = append(out, Secret{Kind: rule.kind, Masked: masked, Hash: h})
		}
	}
	return out
}

// SensitivePath reports whether a path is a place secrets live, and why.
func SensitivePath(p string) (string, bool) {
	slash := strings.ReplaceAll(p, `\`, "/")
	base := strings.ToLower(path.Base(slash))
	switch {
	case base == ".env" || (strings.HasPrefix(base, ".env.") && !strings.Contains(base, "example") && !strings.Contains(base, "sample") && !strings.Contains(base, "template")):
		return "environment file with secrets", true
	case base == "id_rsa" || base == "id_ed25519" || base == "id_ecdsa" || base == "id_dsa":
		return "SSH private key", true
	case strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx"):
		return "private key or certificate", true
	case base == ".npmrc" || base == ".pypirc" || base == ".netrc" || base == ".git-credentials" || base == ".dockercfg":
		return "credentials file", true
	case strings.HasSuffix(base, ".kdbx"):
		return "password database", true
	case base == "login data" || base == "cookies" || base == "web data":
		return "browser passwords or cookies", true
	}
	for _, dir := range []string{"/.ssh/", "/.aws/", "/.gnupg/", "/.config/gcloud/", "/.azure/", "/.kube/", "/.docker/config.json"} {
		if strings.Contains(strings.ToLower(slash)+"/", dir) {
			return "credentials folder", true
		}
	}
	return "", false
}

// SecretKinds summarises secrets as "Stripe key, 2× Password or secret".
func SecretKinds(ss []Secret) string {
	count := map[string]int{}
	var order []string
	for _, s := range ss {
		if count[s.Kind] == 0 {
			order = append(order, s.Kind)
		}
		count[s.Kind]++
	}
	sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
	parts := make([]string, len(order))
	for i, k := range order {
		if count[k] > 1 {
			parts[i] = strconv.Itoa(count[k]) + "× " + k
		} else {
			parts[i] = k
		}
	}
	return strings.Join(parts, ", ")
}
