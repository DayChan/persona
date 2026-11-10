package screen

import (
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// secretRE are password prompts as the last line of a screen. Deliberately narrow (after Ovis's secret-prompt.ts):
// prose that mentions passwords must not turn a card into a password field.
var secretRE = []*lazyre.Regexp{
	lazyre.New(`(?i)^\[sudo\] password for [^:\s]+:$`),
	// su; ssh's me@host's password:
	lazyre.New(`(?i)^(?:\S+'s )?password:$`),
	// ssh keys, gpg
	lazyre.New(`(?i)^enter passphrase(?: for (?:key )?.+)?:$`),
	// hardware tokens
	lazyre.New(`(?i)^(?:enter )?(?:pin|passcode)(?: for .+)?:$`),
	// git over https: Password for 'https://me@host':
	lazyre.New(`(?i)^password for (?:'[^']+'|[^\s:']+):$`),
	lazyre.New(`(?i)^\S+@\S+ password:$`),
	// mysql -p, passwd, ssh-keygen, ansible-vault, openssl (Verifying - ...), keytool: Enter password:,
	// Retype new password:, Enter same passphrase again:, Vault password:, Enter PEM pass phrase:, Password (again):
	lazyre.New(`(?i)^(?:verifying - )?(?:(?:enter|retype|re-enter|confirm|repeat|verify)\s+)?(?:(?:new|current|old|same|your)\s+)?` +
		`(?:(?:pem|keystore|key|vault|unix|ldap|database|import|export)\s+)?(?:password|passphrase|pass phrase)(?:\s*\([^)]*\))?(?:\s+again)?:$`),
	// psql: Password for user postgres:
	lazyre.New(`(?i)^password for user [^\s:]+:$`),
}

// SecretPrompt: the screen ends in a password prompt, where what is typed must not be kept or logged.
func SecretPrompt(plain string) bool {
	lines := strings.Split(strings.ReplaceAll(plain, "\r", ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(strings.ReplaceAll(lines[i], " ", " "))
		if t == "" {
			continue
		}
		for _, re := range secretRE {
			if re.MatchString(t) {
				return true
			}
		}
		return false
	}
	return false
}
