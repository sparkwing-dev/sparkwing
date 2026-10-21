package capability

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPaymentPullHistoricalProviderFactsAndPagination(t *testing.T) {
	v, _ := providerVerifier(t, PaymentTest)
	var cursors []string
	v.provider.client.Transport = paymentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.stripe.com" || r.Header.Get("Authorization") != "Bearer sk_test_private" || r.Header.Get("Stripe-Version") != PaymentProviderAPIVersion {
			t.Fatalf("unsafe request %v", r)
		}
		data := `[]`
		more := false
		if r.URL.Path == "/v1/refunds" {
			cursor := r.URL.Query().Get("starting_after")
			cursors = append(cursors, cursor)
			switch cursor {
			case "":
				more = true
				data = `[{"id":"re_old","object":"refund","created":1,"livemode":false,"payment_intent":"pi_paid","charge":"ch_paid","status":"succeeded","amount":600,"currency":"usd"}]`
			case "re_old":
				data = `[{"id":"re_new","object":"refund","created":2,"livemode":false,"payment_intent":"pi_paid","charge":"ch_paid","status":"pending","amount":100,"currency":"usd"}]`
			default:
				t.Fatalf("cursor %q", cursor)
			}
		}
		body := fmt.Sprintf(`{"object":"list","has_more":%t,"data":%s}`, more, data)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	var first []PaymentEvent
	for sweep := 0; sweep < 2; sweep++ {
		var got []PaymentEvent
		err := v.PullPayments(t.Context(), testPaymentScope, func(proof VerifiedPaymentEvent) error { got = append(got, proof.Event()); return nil })
		if err != nil || len(got) != 2 {
			t.Fatalf("events=%+v err=%v", got, err)
		}
		if got[0].Created != 1 || got[0].ObjectID != "re_old" || !strings.HasPrefix(got[0].EventID, "pull_") {
			t.Fatalf("old payment missing: %+v", got)
		}
		if sweep == 0 {
			first = got
		} else if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("repeated sweep changed identities: %+v", got)
		}
	}
	if strings.Join(cursors, ",") != ",re_old,,re_old" {
		t.Fatalf("pagination %q", cursors)
	}
}

func TestPaymentPullRejectsModeAndPropagatesSettlementFailure(t *testing.T) {
	for _, live := range []bool{true, false} {
		v, _ := providerVerifier(t, PaymentTest)
		v.provider.client.Transport = paymentTransport(func(r *http.Request) (*http.Response, error) {
			data := []map[string]any{}
			if r.URL.Path == "/v1/refunds" {
				data = append(data, map[string]any{"id": "re_old", "object": "refund", "created": 1, "livemode": live, "payment_intent": "pi_paid", "charge": "ch_paid", "status": "succeeded", "amount": 600, "currency": "usd"})
			}
			body, _ := json.Marshal(map[string]any{"object": "list", "has_more": false, "data": data})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})
		calls := 0
		err := v.PullPayments(t.Context(), testPaymentScope, func(VerifiedPaymentEvent) error { calls++; return fmt.Errorf("settlement refused") })
		if err == nil || live && calls != 0 || !live && calls != 1 {
			t.Fatalf("live=%t calls=%d err=%v", live, calls, err)
		}
	}
}

func TestPaymentPullRefusesIncompleteProviderListing(t *testing.T) {
	for _, body := range []string{
		`{"object":"list","has_more":true,"data":[]}`,
		`{"object":"list","data":[]}`,
		`{"object":"list","has_more":false}`,
		`{"object":"list","has_more":false,"data":[{"id":"cs_wrong_mode","object":"checkout.session","livemode":true}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			v, _ := providerVerifier(t, PaymentTest)
			v.provider.client.Transport = paymentTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			err := v.PullPayments(t.Context(), testPaymentScope, func(VerifiedPaymentEvent) error {
				t.Fatal("incomplete provider listing reached settlement")
				return nil
			})
			if err == nil {
				t.Fatal("incomplete provider listing completed a restore sweep")
			}
		})
	}
}

func TestPaymentPullSetupCompletionAndDetachedHistory(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprint(detached), func(t *testing.T) {
			v, _ := providerVerifier(t, PaymentTest)
			v.provider.client.Transport = paymentTransport(func(r *http.Request) (*http.Response, error) {
				data := `[]`
				switch r.URL.Path {
				case "/v1/setup_intents":
					data = `[{"id":"seti_A","object":"setup_intent","created":10,"livemode":false,"status":"succeeded","customer":"cus_owner","payment_method":"pm_A","metadata":{"purpose":"card_setup"}},{"id":"seti_B","object":"setup_intent","created":20,"livemode":false,"status":"succeeded","customer":"cus_owner","payment_method":"pm_B","metadata":{"purpose":"card_setup"}}]`
				case "/v1/events":
					data = `[{"id":"evt_A","object":"event","type":"setup_intent.succeeded","created":40,"livemode":false,"data":{"object":{"id":"seti_A"}}},{"id":"evt_B","object":"event","type":"setup_intent.succeeded","created":30,"livemode":false,"data":{"object":{"id":"seti_B"}}}]`
				case "/v1/payment_methods/pm_A", "/v1/payment_methods/pm_B":
					customer := `"cus_owner"`
					if detached {
						customer = `null`
					}
					body := fmt.Sprintf(`{"id":%q,"object":"payment_method","livemode":false,"customer":%s,"type":"card","card":{"fingerprint":"fp","brand":"visa","last4":"4242"}}`, strings.TrimPrefix(r.URL.Path, "/v1/payment_methods/"), customer)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				case "/v1/refunds":
					data = `[{"id":"re_after_setup","object":"refund","created":50,"livemode":false,"payment_intent":"pi_paid","charge":"ch_paid","status":"succeeded","amount":600,"currency":"usd"}]`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","has_more":false,"data":` + data + `}`))}, nil
			})
			var got []PaymentEvent
			if err := v.PullPayments(t.Context(), testPaymentScope, func(proof VerifiedPaymentEvent) error { got = append(got, proof.Event()); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 || got[0].Created != 40 || got[1].Created != 30 || got[0].MethodDetached != detached || got[1].MethodDetached != detached || got[2].ObjectID != "re_after_setup" {
				t.Fatalf("completion order or money recovery lost: %+v", got)
			}
		})
	}
}

func TestPaymentPullMissingCompletionStillRecoversMoney(t *testing.T) {
	v, _ := providerVerifier(t, PaymentTest)
	v.provider.client.Transport = paymentTransport(func(r *http.Request) (*http.Response, error) {
		data := `[]`
		switch r.URL.Path {
		case "/v1/setup_intents":
			data = `[{"id":"seti_old","object":"setup_intent","created":1,"livemode":false,"status":"succeeded","customer":"cus_owner","payment_method":"pm_card","metadata":{"purpose":"card_setup"}}]`
		case "/v1/refunds":
			data = `[{"id":"re_later","object":"refund","created":50,"livemode":false,"payment_intent":"pi_paid","charge":"ch_paid","status":"succeeded","amount":600,"currency":"usd"}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","has_more":false,"data":` + data + `}`))}, nil
	})
	var got []PaymentEvent
	err := v.PullPayments(t.Context(), testPaymentScope, func(proof VerifiedPaymentEvent) error { got = append(got, proof.Event()); return nil })
	if err == nil || !strings.Contains(err.Error(), "completion evidence") || len(got) != 1 || got[0].ObjectID != "re_later" {
		t.Fatalf("missing completion guessed card order or stopped money recovery: events=%+v err=%v", got, err)
	}
}
