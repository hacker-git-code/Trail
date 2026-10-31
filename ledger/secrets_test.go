package ledger

import (
	"strings"
	"testing"
)

func kinds(ss []Secret) string {
	var k []string
	for _, s := range ss {
		k = append(k, s.Kind)
	}
	return strings.Join(k, ",")
}

func TestScanSecretsFindsRealKeys(t *testing.T) {
	env := "\nANTHROPIC_API_KEY=" + fakeAnthropic +
		"\nOPENAI_API_KEY=" + fakeOpenAI +
		"\nSTRIPE_SECRET_KEY=" + fakeStripe +
		"\nGITHUB_TOKEN=" + fakeGitHub +
		"\nDATABASE_URL=postgres://admin:Tr4ilR0cks!92@db.example.com:5432/app" +
		"\nAWS_ACCESS_KEY_ID=" + fakeAWS +
		"\n-----BEGIN OPENSSH PRIVATE KEY-----\n"
	got := ScanSecrets([]byte(env))
	want := []string{"Anthropic API key", "OpenAI API key", "GitHub token", "AWS access key", "Stripe key", "Private key", "Database URL with password"}
	for _, w := range want {
		if !strings.Contains(kinds(got), w) {
			t.Errorf("missed %s; got %s", w, kinds(got))
		}
	}
	for _, s := range got {
		if strings.Contains(s.Masked, "Zk3n9QpLmX7vT2wR8yB4") || len(s.Hash) != 16 {
			t.Errorf("secret not masked: %+v", s)
		}
	}
	// the Anthropic key must not also count as an OpenAI key, and the
	// specific keys must not be double-counted by the generic rule
	if strings.Count(kinds(got), "API key") != 2 {
		t.Errorf("double counted: %s", kinds(got))
	}
}

func TestScanSecretsIgnoresPlaceholdersAndCode(t *testing.T) {
	clean := `
API_KEY=your_api_key_here
password = os.environ["DB_PASSWORD"]
token: ${{ secrets.GITHUB_TOKEN }}
secret = "<your secret>"
api_key = "abc"
PASSWORD=xxxxxxxxxxxx
const token = process.env.TOKEN
`
	if got := ScanSecrets([]byte(clean)); len(got) != 0 {
		t.Errorf("false positives: %+v", got)
	}
}

func TestScanSecretsGenericPassword(t *testing.T) {
	got := ScanSecrets([]byte(`db_password: "N8#vq2!Lm9zR"`))
	if kinds(got) != "Password or secret" {
		t.Errorf("got %s", kinds(got))
	}
}

func TestSensitivePath(t *testing.T) {
	yes := []string{"/p/.env", "/p/.env.local", "/h/.ssh/id_rsa", "/h/.aws/credentials", "C:\\Users\\r\\.ssh\\config", "/k/server.pem", "/h/.npmrc"}
	no := []string{"/p/.env.example", "/p/README.md", "/p/src/env.ts", "/p/keyboard.go"}
	for _, p := range yes {
		if _, ok := SensitivePath(p); !ok {
			t.Errorf("%s should be sensitive", p)
		}
	}
	for _, p := range no {
		if why, ok := SensitivePath(p); ok {
			t.Errorf("%s flagged as %s", p, why)
		}
	}
}
