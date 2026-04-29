package main

import plaid "github.com/plaid/plaid-go/v29/plaid"

// plaidErrorClassMap maps Plaid "ErrorType/ErrorCode" (composite) or "ErrorType" alone
// to schema v2 event_class values.
var plaidErrorClassMap = map[string]string{
	// Auth failures
	"ITEM_ERROR/ITEM_LOGIN_REQUIRED":  "vendor_auth_required",
	"ITEM_ERROR/INVALID_ACCESS_TOKEN": "vendor_auth_required",

	// Institution availability
	"INSTITUTION_ERROR/INSTITUTION_DOWN":                "vendor_unavailable",
	"INSTITUTION_ERROR/INSTITUTION_NOT_RESPONDING":      "vendor_unavailable",
	"INSTITUTION_ERROR/INSTITUTION_NO_LONGER_SUPPORTED": "vendor_unavailable",

	// Rate limiting (ErrorType == ErrorCode for this class)
	"RATE_LIMIT_EXCEEDED": "vendor_rate_limited",

	// Data errors
	"TRANSACTIONS_ERROR": "vendor_data_error",
}

// plaidErrorCodeMap provides code-only fallback for cases where the ErrorType is generic
// (e.g. the Plaid SDK encodes diverse codes under "ITEM_ERROR").
var plaidErrorCodeMap = map[string]string{
	"ITEM_LOGIN_REQUIRED":             "vendor_auth_required",
	"INVALID_ACCESS_TOKEN":            "vendor_auth_required",
	"INSTITUTION_DOWN":                "vendor_unavailable",
	"INSTITUTION_NOT_RESPONDING":      "vendor_unavailable",
	"INSTITUTION_NO_LONGER_SUPPORTED": "vendor_unavailable",
	"RATE_LIMIT_EXCEEDED":             "vendor_rate_limited",
	"TRANSACTIONS_ERROR":              "vendor_data_error",
}

// classifyPlaidError returns the schema v2 event_class for a Plaid API error.
// Lookup order: composite "ErrorType/ErrorCode" → "ErrorType" alone → "ErrorCode" alone → "vendor_unknown".
// Never panics.
func classifyPlaidError(pe plaid.PlaidError) string {
	if class, ok := plaidErrorClassMap[string(pe.ErrorType)+"/"+pe.ErrorCode]; ok {
		return class
	}
	if class, ok := plaidErrorClassMap[string(pe.ErrorType)]; ok {
		return class
	}
	if class, ok := plaidErrorCodeMap[pe.ErrorCode]; ok {
		return class
	}
	return "vendor_unknown"
}
