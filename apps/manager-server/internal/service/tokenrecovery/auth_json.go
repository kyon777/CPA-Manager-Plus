package tokenrecovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

var (
	ErrCredentialInvalid       = errors.New("credential JSON is invalid")
	ErrCredentialNotFound      = errors.New("credential record was not found")
	ErrCredentialAmbiguous     = errors.New("credential record is ambiguous")
	ErrCredentialEmailMismatch = errors.New("credential email does not match")
	ErrIncompleteTokenResult   = errors.New("token result is incomplete")
)

// Locator identifies one credential inside a physical auth JSON file. Account
// IDs are deliberately absent: recovery only accepts matching email evidence.
type Locator struct {
	AuthIndex    string
	AccountEmail string
}

// AcquisitionResult contains only the external result fields needed for the
// in-memory credential merge. It must never be serialized into task state or a
// browser response.
type AcquisitionResult struct {
	Email            string
	AccessToken      string
	RefreshToken     string
	IDToken          string
	ChatGPTAccountID string
}

// Credential is the non-secret routing data extracted from an auth record.
// HTTPProxy is retained only long enough to make the outbound acquisition
// request and must not be persisted or logged.
type Credential struct {
	Email     string
	HTTPProxy string
}

type authDocument struct {
	root       any
	record     map[string]any
	credential Credential
}

var authIndexFieldNames = []string{"auth_index", "authIndex", "auth-index"}
var authEmailFieldNames = []string{"email", "account", "account_email", "accountEmail", "username"}
var proxyFieldNames = []string{"proxy_url", "proxyUrl", "proxy-url"}

// ReadCredential parses a physical CPA auth JSON file and returns the target
// email and optional HTTP proxy. It accepts a single object or an array of
// records. For arrays, auth_index is authoritative; email-only selection must
// resolve to exactly one record.
func ReadCredential(raw []byte, locator Locator) (Credential, error) {
	doc, err := parseAuthDocument(raw, locator)
	if err != nil {
		return Credential{}, err
	}
	return doc.credential, nil
}

// ReadProxyURL returns the proxy configured on the selected credential record
// without applying the HTTP-only transport validation used by TokenAcquisition.
// A physical file containing one object can be read without a locator; an array
// always needs a locator because its record identity would otherwise be unsafe.
func ReadProxyURL(raw []byte, locator Locator) (string, error) {
	doc, err := parseAuthDocumentForRead(raw, locator)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(firstNonEmptyField(doc.record, proxyFieldNames...)), nil
}

// MergeAuthJSON returns a copy of the original root JSON with only the target
// token fields changed. All unrecognized fields are retained verbatim at the
// JSON-value level, including note, priority, proxy settings and metadata.
func MergeAuthJSON(raw []byte, locator Locator, result AcquisitionResult) ([]byte, error) {
	doc, err := parseAuthDocument(raw, locator)
	if err != nil {
		return nil, err
	}
	if err := validateResultEmailAndTokens(doc.credential.Email, result); err != nil {
		return nil, err
	}

	doc.record["access_token"] = strings.TrimSpace(result.AccessToken)
	doc.record["refresh_token"] = strings.TrimSpace(result.RefreshToken)
	doc.record["id_token"] = strings.TrimSpace(result.IDToken)
	if accountID := strings.TrimSpace(result.ChatGPTAccountID); accountID != "" {
		synchronizeAccountIDAliases(doc.record, accountID)
	}

	merged, err := json.Marshal(doc.root)
	if err != nil {
		return nil, fmt.Errorf("marshal merged credential: %w", err)
	}
	return merged, nil
}

// synchronizeAccountIDAliases keeps all account-id evidence that already
// exists inside the selected credential record coherent with the newly
// acquired ChatGPT account id. CPA's identity reader also checks the common
// nested metadata containers, so leaving an old nested alias would make the
// freshly written credential appear internally contradictory.
func synchronizeAccountIDAliases(record map[string]any, accountID string) {
	record["chatgpt_account_id"] = accountID
	synchronizeNestedAccountIDAliases(record, accountID)
}

func synchronizeNestedAccountIDAliases(record map[string]any, accountID string) {
	for _, alias := range []string{"account_id", "accountId", "chatgpt_account_id", "chatgptAccountId"} {
		if _, exists := record[alias]; exists {
			record[alias] = accountID
		}
	}
	for _, key := range []string{"id_token", "metadata", "attributes"} {
		child, ok := record[key].(map[string]any)
		if ok {
			synchronizeNestedAccountIDAliases(child, accountID)
		}
	}
}

// VerifyMergedAuthJSON confirms the exact selected record still has the
// expected email and token values after CPA Core accepted an upload.
func VerifyMergedAuthJSON(raw []byte, locator Locator, result AcquisitionResult) error {
	doc, err := parseAuthDocument(raw, locator)
	if err != nil {
		return err
	}
	if err := validateResultEmailAndTokens(doc.credential.Email, result); err != nil {
		return err
	}
	if stringValue(doc.record["access_token"]) != strings.TrimSpace(result.AccessToken) ||
		stringValue(doc.record["refresh_token"]) != strings.TrimSpace(result.RefreshToken) ||
		stringValue(doc.record["id_token"]) != strings.TrimSpace(result.IDToken) {
		return ErrCredentialInvalid
	}
	if accountID := strings.TrimSpace(result.ChatGPTAccountID); accountID != "" &&
		stringValue(doc.record["chatgpt_account_id"]) != accountID {
		return ErrCredentialInvalid
	}
	return nil
}

func parseAuthDocument(raw []byte, locator Locator) (authDocument, error) {
	locator.AuthIndex = strings.TrimSpace(locator.AuthIndex)
	locator.AccountEmail = normalizeEmail(locator.AccountEmail)
	if locator.AuthIndex == "" && locator.AccountEmail == "" {
		return authDocument{}, ErrCredentialNotFound
	}

	root, err := decodeAuthDocumentRoot(raw)
	if err != nil {
		return authDocument{}, err
	}

	record, err := locateRecord(root, locator)
	if err != nil {
		return authDocument{}, err
	}
	email := firstNonEmptyField(record, authEmailFieldNames...)
	normalizedEmail := normalizeEmail(email)
	if normalizedEmail == "" {
		return authDocument{}, ErrCredentialNotFound
	}
	if locator.AccountEmail != "" && normalizedEmail != locator.AccountEmail {
		return authDocument{}, ErrCredentialEmailMismatch
	}
	return authDocument{
		root:   root,
		record: record,
		credential: Credential{
			Email:     strings.TrimSpace(email),
			HTTPProxy: extractHTTPProxy(record),
		},
	}, nil
}

func parseAuthDocumentForRead(raw []byte, locator Locator) (authDocument, error) {
	locator.AuthIndex = strings.TrimSpace(locator.AuthIndex)
	locator.AccountEmail = normalizeEmail(locator.AccountEmail)
	if locator.AuthIndex != "" || locator.AccountEmail != "" {
		return parseAuthDocument(raw, locator)
	}

	root, err := decodeAuthDocumentRoot(raw)
	if err != nil {
		return authDocument{}, err
	}
	record, ok := root.(map[string]any)
	if ok {
		return authDocument{root: root, record: record}, nil
	}
	if _, ok := root.([]any); ok {
		return authDocument{}, ErrCredentialAmbiguous
	}
	return authDocument{}, ErrCredentialInvalid
}

func decodeAuthDocumentRoot(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, ErrCredentialInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrCredentialInvalid
	}
	return root, nil
}

func locateRecord(root any, locator Locator) (map[string]any, error) {
	switch typed := root.(type) {
	case map[string]any:
		if locator.AuthIndex != "" {
			// A single physical credential file may not carry CPA's runtime
			// auth_index inside its JSON. In that case the email check performed
			// by parseAuthDocument remains the acceptance fence. If the document
			// does explicitly carry an index, however, it must still match.
			documentAuthIndex := firstNonEmptyField(typed, authIndexFieldNames...)
			if documentAuthIndex != "" && documentAuthIndex != locator.AuthIndex {
				return nil, ErrCredentialNotFound
			}
		}
		return typed, nil
	case []any:
		candidates := make([]map[string]any, 0, len(typed))
		for _, value := range typed {
			record, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if locator.AuthIndex != "" {
				if firstNonEmptyField(record, authIndexFieldNames...) == locator.AuthIndex {
					candidates = append(candidates, record)
				}
				continue
			}
			if normalizeEmail(firstNonEmptyField(record, authEmailFieldNames...)) == locator.AccountEmail {
				candidates = append(candidates, record)
			}
		}
		switch len(candidates) {
		case 0:
			return nil, ErrCredentialNotFound
		case 1:
			return candidates[0], nil
		default:
			return nil, ErrCredentialAmbiguous
		}
	default:
		return nil, ErrCredentialInvalid
	}
}

func validateResultEmailAndTokens(originalEmail string, result AcquisitionResult) error {
	if normalizeEmail(originalEmail) == "" || normalizeEmail(result.Email) != normalizeEmail(originalEmail) {
		return ErrCredentialEmailMismatch
	}
	if strings.TrimSpace(result.AccessToken) == "" ||
		strings.TrimSpace(result.RefreshToken) == "" ||
		strings.TrimSpace(result.IDToken) == "" {
		return ErrIncompleteTokenResult
	}
	return nil
}

func extractHTTPProxy(record map[string]any) string {
	proxy := strings.TrimSpace(firstNonEmptyField(record, proxyFieldNames...))
	if proxy == "" {
		return ""
	}
	parsed, err := url.Parse(proxy)
	if err != nil || !strings.EqualFold(parsed.Scheme, "http") || strings.TrimSpace(parsed.Host) == "" {
		return ""
	}
	return proxy
}

func firstNonEmptyField(record map[string]any, names ...string) string {
	for _, name := range names {
		value := stringValue(record[name])
		if value == "" {
			if number, ok := record[name].(json.Number); ok {
				value = number.String()
			}
		}
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
