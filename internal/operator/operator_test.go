package operator

import (
	"path/filepath"
	"strings"
	"testing"
)

// A passphrase on the command line is disclosed to every process on the
// machine. It is refused rather than warned about: a warning on a credential
// path is a credential leak with a note attached.
func TestAPassphraseInArgvIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"register", "--passphrase", "hunter2hunter2"},
		{"register", "--passphrase=hunter2hunter2"},
		{"init", "--password=hunter2hunter2"},
		{"init", "--PASS=hunter2hunter2"},
	} {
		err := RefusePassphraseInArgv(args)
		if err == nil {
			t.Errorf("%v was accepted", args)
			continue
		}
		if !strings.Contains(err.Error(), "stdin") {
			t.Errorf("%v refused without naming the alternative: %v", args, err)
		}
	}
	if err := RefusePassphraseInArgv([]string{"register", "--orch", "http://x", "--role", "admin"}); err != nil {
		t.Errorf("an ordinary command was refused: %v", err)
	}
}

// One machine may serve several operator identities, so the location is
// parameterised. A shared path would have a second account use or overwrite the
// first account's identity.
func TestKeysDirIsParameterised(t *testing.T) {
	t.Setenv(EnvKeysDir, "/tmp/env-identity")
	SetKeysDir("")
	if got := keysDirectory(); got != "/tmp/env-identity" {
		t.Errorf("environment ignored: %q", got)
	}
	SetKeysDir("/tmp/flag-identity")
	if got := keysDirectory(); got != "/tmp/flag-identity" {
		t.Errorf("--keys-dir did not take precedence: %q", got)
	}
	// The token belongs to the identity that obtained it, so it lives beside it.
	if got := tokenFilePath(); got != filepath.Join("/tmp/flag-identity", "token") {
		t.Errorf("token is not beside its identity: %q", got)
	}
	SetKeysDir("")
}

func TestKeysDirFlagIsConsumedNotPassedOn(t *testing.T) {
	SetKeysDir("")
	rest := takeKeysDir([]string{"register", "--keys-dir", "/tmp/a", "--orch", "http://x"})
	if strings.Join(rest, " ") != "register --orch http://x" {
		t.Errorf("flag not consumed: %v", rest)
	}
	if keysDirectory() != "/tmp/a" {
		t.Errorf("flag not applied: %q", keysDirectory())
	}
	SetKeysDir("")
}
