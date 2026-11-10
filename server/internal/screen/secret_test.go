package screen

import "testing"

func TestSecretPrompt(t *testing.T) {
	for _, s := range []string{
		"$ sudo make install\n[sudo] password for me: ",
		"Password:",
		"me@build.example's password: \n",
		"Enter passphrase for key '/home/me/.ssh/id_ed25519':",
		"Password for 'https://me@git.example.com':",
		"Enter PIN for token:",
		"$ mysql -u root -p\nEnter password: ",
		"$ psql -h db.example -U postgres\nPassword for user postgres: ",
		"$ passwd\nChanging password for me.\nCurrent password: ",
		"$ passwd\nNew password: ",
		"$ passwd\nNew password: \nRetype new password: ",
		"$ ssh-keygen -t ed25519\nEnter passphrase (empty for no passphrase): ",
		"Enter passphrase (empty for no passphrase): \nEnter same passphrase again: ",
		"$ ansible-vault edit secrets.yml\nVault password: ",
		"$ openssl genrsa -aes256 -out key.pem\nEnter PEM pass phrase:",
		"Enter PEM pass phrase:\nVerifying - Enter PEM pass phrase:",
		"$ keytool -genkeypair -keystore app.jks\nEnter keystore password: ",
		"Re-enter new password: ",
		"Password (again): ",
	} {
		if !SecretPrompt(s) {
			t.Errorf("not a password prompt: %q", s)
		}
	}
	for _, s := range []string{
		"⏺ The password field now validates length.\n❯ ",
		"Password: must be at least 12 characters",
		"Do you want to change the password?\n ❯ 1. Yes\n   2. No",
		"[sudo] password for me:\nSorry, try again.\n$ ",
		"The new password: see below\n$ ",
		"The new password:",
		"Change password?",
		"New password:\npasswd: password updated successfully\n$ ",
		"Enter password: wrong, try again",
		"Password for user postgres: authentication failed",
		"",
	} {
		if SecretPrompt(s) {
			t.Errorf("taken for a password prompt: %q", s)
		}
	}
}
