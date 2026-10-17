package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ADR-115 messaging credentials (`realm-id messaging update`). The body carries
// WRITE-ONLY provider secrets, so this resource has its own rule for where a
// secret may come from (owner ruling 2026-10-17): never a flag. A flag value
// lands in shell history and in `ps`; stdin and a no-echo prompt do neither.

// secretFields are the write-only fields of MessagingChannelPut.
var secretFields = []string{"auth_token", "api_key_secret", "access_token"}

// channelSecretField is the field `--secret-stdin` and the prompt fill, per
// channel. sms takes auth_token (the api_key_sid + api_key_secret pair is sent
// as JSON on stdin); whatsapp has the single access_token.
var channelSecretField = map[string]string{"sms": "auth_token", "whatsapp": "access_token"}

// secretPrompt reads a secret without echo. nil in production, where
// promptSecret uses the controlling terminal; tests inject a reader here.
var secretPrompt func() (string, error)

func isMessagingPut(cmd command) bool {
	return cmd.Method == "PUT" && strings.Contains(cmd.Path, "/messaging/")
}

func isSecretField(k string) bool {
	for _, s := range secretFields {
		if k == s {
			return true
		}
	}
	return false
}

// refuseFlagSecret names the key only, never the value.
func refuseFlagSecret(key, via string) error {
	return fmt.Errorf("%s is a write-only secret and is refused via %s (it would land in shell history and `ps`); "+
		"pipe it on stdin: `--secret-stdin < token.txt`, or JSON on stdin, or run interactively for a hidden prompt", key, via)
}

func buildMessagingBody(cmd command, pf *parsedFlags) (any, error) {
	for _, f := range pf.fields {
		k := f
		if eq := strings.IndexByte(f, '='); eq >= 0 {
			k = strings.TrimSuffix(f[:eq], ":")
		}
		if isSecretField(k) {
			return nil, refuseFlagSecret(k, "--field")
		}
	}
	if raw, ok := pf.vals["json"]; ok {
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			for k := range m {
				if isSecretField(k) {
					return nil, refuseFlagSecret(k, "--json")
				}
			}
		}
	}

	_, secretStdin := pf.vals["secret-stdin"]
	var body any
	if _, hasJSON := pf.vals["json"]; hasJSON || len(pf.fields) > 0 || !secretStdin {
		b, err := buildPlainBody(cmd, pf)
		if err != nil {
			return nil, err
		}
		body = b
	}
	obj, _ := body.(map[string]any)
	if body != nil && obj == nil {
		return body, nil // not an object: let the server say so
	}
	if obj == nil {
		obj = map[string]any{}
	}

	field := channelSecretField[pf.vals["channel"]]
	if secretStdin {
		if field == "" {
			return nil, fmt.Errorf("--secret-stdin needs --channel sms|whatsapp")
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("reading the secret from stdin: %w", err)
			}
			return nil, fmt.Errorf("--secret-stdin: stdin carried no secret")
		}
		obj[field] = line
		return obj, nil
	}

	for _, k := range secretFields {
		if _, ok := obj[k]; ok {
			return obj, nil // a secret arrived on stdin JSON
		}
	}
	if field != "" && (secretPrompt != nil || term.IsTerminal(int(os.Stdin.Fd()))) {
		sec, err := promptSecret()
		if err != nil {
			return nil, fmt.Errorf("reading the secret: %w", err)
		}
		if sec == "" {
			return nil, fmt.Errorf("no secret entered")
		}
		obj[field] = sec
	}
	return obj, nil
}

func promptSecret() (string, error) {
	if secretPrompt != nil {
		return secretPrompt()
	}
	fmt.Fprint(os.Stderr, "Secret (input hidden): ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(string(b)), err
}

// messagingGuide is `realm-id explain messaging`.
const messagingGuide = `realm-id messaging - your realm's own SMS (Twilio) / WhatsApp (Meta) credentials (ADR-115)

  realm-id messaging describe --channel sms|whatsapp     read config + status (never a secret)
  realm-id messaging update   --channel sms|whatsapp     save credentials (replaces the row whole)
  realm-id messaging revoke   --channel sms|whatsapp     remove them (idempotent)

Secrets (auth_token, api_key_secret, access_token) are write-only and are REFUSED as
--field or --json: a flag value lands in shell history and in ps. Give the secret by:
  1. --secret-stdin   one line on stdin becomes the channel's secret
                      (sms -> auth_token, whatsapp -> access_token)
  2. JSON on stdin    the whole body, for the sms api_key_sid + api_key_secret pair
  3. a hidden prompt  automatic when stdin is a terminal and no secret was given
Non-secret fields stay --field.

  realm-id messaging update --channel sms --field account_sid=AC... --field from_number=+1555... --secret-stdin < token.txt

The provider is asked whether the credentials work before anything is stored: a refusal
is 422 channel_validation_failed and nothing is saved. Secrets must be at least 20 characters.
`

func explainMessaging(asJSON bool) int {
	if asJSON {
		res := struct {
			helperEnvelope
			Topic string `json:"topic"`
			Text  string `json:"text"`
		}{newEnvelope("explain"), "messaging", messagingGuide}
		res.OK = true
		writeJSON(res)
		return exitOK
	}
	fmt.Print(messagingGuide)
	return exitOK
}
