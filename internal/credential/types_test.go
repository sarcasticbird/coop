package credential

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSecretTypesRedactFormattingAndSerialization(t *testing.T) {
	const secret = "do-not-print-this-secret"
	acquired := Acquired{
		payload: []byte(secret),
		aws: &AWSCredentials{
			accessKeyID:     secret,
			secretAccessKey: secret,
			sessionToken:    secret,
		},
	}

	for _, value := range []any{acquired, acquired.aws} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); strings.Contains(got, secret) {
				t.Fatalf("format %s exposed secret: %s", format, got)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("JSON exposed secret: %s", encoded)
		}
	}
}
