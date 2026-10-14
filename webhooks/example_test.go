package webhooks_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/circlexo/circlexo-go/webhooks"
)

func ExampleVerify() {
	// Fictional example-only secret; a receiver uses its configured signing secret.
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Unix(1800000000, 0)
	body := []byte(`{"id":"event-1","type":"org.app_installed","version":1,"data":{}}`)
	signature, err := webhooks.Sign(secret, "event-1", now, body)
	if err != nil {
		panic(err)
	}
	headers := http.Header{}
	headers.Set(webhooks.HeaderID, "event-1")
	headers.Set(webhooks.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	headers.Set(webhooks.HeaderSignature, signature)
	err = webhooks.Verify([]string{secret}, headers, body, webhooks.DefaultTolerance, now)
	fmt.Println("valid:", err == nil)
	// Verify the raw body before decoding or dispatching the event.
	err = webhooks.Verify([]string{secret}, headers, []byte(`{}`), webhooks.DefaultTolerance, now)
	fmt.Println("tampering rejected:", err != nil)
	// Output:
	// valid: true
	// tampering rejected: true
}
