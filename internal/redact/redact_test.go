package redact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretsPatterns(t *testing.T) {
	for input, want := range map[string]string{
		"A=1 ls":                         "A=1 ls", // benign assignment untouched
		"MY_TOKEN=abc ls":                "MY_TOKEN=[REDACTED] ls",
		"--password=x --port 5432":       "--password=[REDACTED] --port 5432",
		"http://u:p@h/x":                 "http://u:[REDACTED]@h/x",
		"Authorization: Bearer tok":      "Authorization: Bearer [REDACTED]",
		"echo add token support to docs": "echo add token support to docs", // prose survives
		// Quoted values are consumed whole, spaces included.
		"--password 'a b c' -h x":  "--password [REDACTED] -h x",
		`PASSWORD="a b c" deploy`:  "PASSWORD=[REDACTED] deploy",
		`--token="multi word" run`: "--token=[REDACTED] run",
		// The shapes secrets take in review comments and patches.
		"hardcodes postgresql://trading:trading123@localhost:5432/db here": "hardcodes postgresql://trading:[REDACTED]@localhost:5432/db here",
		`+DATABASE_PASSWORD="hunter2"`:                                     "+DATABASE_PASSWORD=[REDACTED]",
		// Escaped quotes stay inside the value — the tail must not leak.
		`PASSWORD="ab\"cd" deploy`: "PASSWORD=[REDACTED] deploy",
		// Code and config assignment shapes: spaces, :=, colon keys.
		`API_TOKEN = "abc"`:        "API_TOKEN = [REDACTED]",
		`dbPassword := "hunter2"`:  "dbPassword := [REDACTED]",
		"password: hunter2":        "password: [REDACTED]",
		`"password": "hunter2"`:    `"password": [REDACTED]`,
		"aws_secret_key: AKIA9999": "aws_secret_key: [REDACTED]",
		// Comparisons are not assignments; prose colons stay prose.
		"if password == provided": "if password == provided",
		"auth: needs a refactor":  "auth: needs a refactor",
	} {
		assert.Equal(t, want, Secrets(input), "input: %s", input)
	}
}
