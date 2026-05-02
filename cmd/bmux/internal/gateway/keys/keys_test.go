// Package keys_test contains contract, behavioral, and regression tests for KeyTranslator.
package keys_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/gateway/keys"
)

// newTranslator returns the default KeyTranslator under test.
func newTranslator() keys.KeyTranslator {
	return keys.NewKeyTranslator()
}

// --- Contract Tests ---

// CT-1: Plain text with literal=false produces a single Literal:true op.
func TestCT1_PlainText(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("ls -la", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "ls -la", Literal: true}}, ops)
}

// CT-2: {Enter} produces a single Literal:false op with tmux key "Enter".
func TestCT2_Enter(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("{Enter}", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "Enter", Literal: false}}, ops)
}

// CT-3: {Ctrl-C} produces exactly "C-c" (RG-1).
func TestCT3_CtrlC(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("{Ctrl-C}", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "C-c", Literal: false}}, ops)
}

// CT-4: Mixed "ls{Enter}" produces 2 ops: plain text then Enter.
func TestCT4_Mixed(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("ls{Enter}", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{
		{Keys: "ls", Literal: true},
		{Keys: "Enter", Literal: false},
	}, ops)
}

// CT-5: Unknown {F13} with literal=false returns error E03.
func TestCT5_UnknownToken(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("{F13}", false)
	require.Nil(t, ops)
	require.Error(t, err)
	var te *keys.TranslateError
	require.ErrorAs(t, err, &te)
	require.Equal(t, "input_invalid_token", te.Code)
}

// CT-6: Input >4096 bytes returns error E01.
func TestCT6_TooLong(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate(strings.Repeat("a", 4097), false)
	require.Nil(t, ops)
	require.Error(t, err)
	var te *keys.TranslateError
	require.ErrorAs(t, err, &te)
	require.Equal(t, "input_invalid_length", te.Code)
}

// CT-7: NUL byte in input returns error E02.
func TestCT7_NulByte(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("hello\x00world", false)
	require.Nil(t, ops)
	require.Error(t, err)
	var te *keys.TranslateError
	require.ErrorAs(t, err, &te)
	require.Equal(t, "input_invalid_nul", te.Code)
}

// CT-8: literal=true with unknown token passes through without error.
func TestCT8_LiteralBypassesTokenParsing(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("{F13} unknown", true)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "{F13} unknown", Literal: true}}, ops)
}

// CT-9: Token matching is case-insensitive.
func TestCT9_CaseInsensitive(t *testing.T) {
	kt := newTranslator()

	lower, err := kt.Translate("{enter}", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "Enter", Literal: false}}, lower)

	upper, err := kt.Translate("{ENTER}", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "Enter", Literal: false}}, upper)
}

// CT-10: Empty input returns [{Keys:"", Literal:true}], nil.
func TestCT10_EmptyInput(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: "", Literal: true}}, ops)
}

// --- Behavioral Tests ---

// BT-1: All 18 allowlisted tokens translate to correct tmux key names.
func TestBT1_AllTokens(t *testing.T) {
	kt := newTranslator()

	cases := []struct {
		input   string
		wantKey string
	}{
		{"{enter}", "Enter"},
		{"{escape}", "Escape"},
		{"{tab}", "Tab"},
		{"{ctrl-c}", "C-c"},
		{"{ctrl-d}", "C-d"},
		{"{ctrl-z}", "C-z"},
		{"{ctrl-l}", "C-l"},
		{"{up}", "Up"},
		{"{down}", "Down"},
		{"{left}", "Left"},
		{"{right}", "Right"},
		{"{pageup}", "PPage"},
		{"{pagedown}", "NPage"},
		{"{home}", "Home"},
		{"{end}", "End"},
		{"{delete}", "DC"},
		{"{backspace}", "BSpace"},
		{"{space}", "Space"},
	}

	require.Len(t, cases, 18, "must test all 18 tokens")

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			ops, err := kt.Translate(tc.input, false)
			require.NoError(t, err)
			require.Equal(t, []keys.SendKeysOp{{Keys: tc.wantKey, Literal: false}}, ops)
		})
	}
}

// BT-2: Multiple consecutive tokens produce one op each.
func TestBT2_ConsecutiveTokens(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("{Ctrl-C}{Enter}", false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{
		{Keys: "C-c", Literal: false},
		{Keys: "Enter", Literal: false},
	}, ops)
}

// BT-3: Text with unknown {not-a-token} in non-literal mode returns E03.
func TestBT3_TextWithUnknownToken(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("echo {not-a-token}", false)
	require.Nil(t, ops)
	require.Error(t, err)
	var te *keys.TranslateError
	require.ErrorAs(t, err, &te)
	require.Equal(t, "input_invalid_token", te.Code)
}

// --- Regression Guards ---

// RG-1: {Ctrl-C} produces exactly "C-c", not "^C" or any other form.
func TestRG1_CtrlCExactString(t *testing.T) {
	kt := newTranslator()
	ops, err := kt.Translate("{Ctrl-C}", false)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, "C-c", ops[0].Keys, "must be exactly C-c (not ^C or ctrl-c)")
	require.False(t, ops[0].Literal)
}

// RG-2: Plain text with shell special chars uses Literal:true.
func TestRG2_ShellSpecialsAreLiteral(t *testing.T) {
	kt := newTranslator()
	input := "echo $HOME && ls"
	ops, err := kt.Translate(input, false)
	require.NoError(t, err)
	require.Equal(t, []keys.SendKeysOp{{Keys: input, Literal: true}}, ops)
}
