package teamblob_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

const bucket = "teamblob-test"

type fakeS3HTTP struct{ http.Handler }

func (f fakeS3HTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.Body != nil && r.ContentLength >= 0 {
		r.Header.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	return w.Result(), nil
}

type listClient struct {
	*s3.Client
	calls      int
	failPrefix string
	failure    error
}

func (c *listClient) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, options ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	c.calls++
	if aws.ToString(in.Prefix) == c.failPrefix && c.failure != nil {
		return nil, c.failure
	}
	return c.Client.ListObjectsV2(ctx, in, options...)
}

type fixture struct {
	raw    *s3.Client
	client *listClient
	store  *teamblob.Store
}

func newFixture(t *testing.T, maxPages int) fixture {
	t.Helper()
	written := gofakes3.FixedTimeSource(time.Now().Add(-365 * 24 * time.Hour))
	handler := gofakes3.New(s3mem.New(s3mem.WithTimeSource(written)), gofakes3.WithTimeSource(written), gofakes3.WithTimeSkewLimit(0)).Server()
	raw := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:  &http.Client{Transport: fakeS3HTTP{handler}},
	})
	if _, err := raw.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	client := &listClient{Client: raw}
	store, err := teamblob.New(teamblob.Options{Bucket: bucket, Prefix: "svc", Client: client, MaxListPages: maxPages})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{raw: raw, client: client, store: store}
}

func (f fixture) put(t *testing.T, key, body string) {
	t.Helper()
	if _, err := f.raw.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body)}); err != nil {
		t.Fatal(err)
	}
}

func TestKeysStayInTheirNamespace(t *testing.T) {
	f := newFixture(t, 0)
	property := func(seed [16]byte, rel string) bool {
		team := fmt.Sprintf("t-%x", seed)
		key, err := f.store.Key(team, rel)
		if err != nil {
			return true
		}
		other, err := f.store.Key(team+"-other", rel)
		return err == nil && key != other && strings.HasPrefix(key, "svc/teams/"+team+"/") && path.Clean(key) == key && !strings.Contains(key, "\\")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(66))}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../other", "a/../../other", "a//b", "./x", "x\\y", "", "/abs", "a/", "x\x00y"} {
		if _, err := f.store.Key("team", rel); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("accepted relative key %q: %v", rel, err)
		}
	}
	for _, rel := range []string{"teams/other/cas/x", "_meta/usage.json"} {
		if _, err := f.store.Key("", rel); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("accepted operator key %q: %v", rel, err)
		}
	}
}

func TestMeasurePreservesOldObjectsAndCountsEachNamespaceOnce(t *testing.T) {
	f := newFixture(t, 0)
	for key, body := range map[string]string{
		"svc/teams/team-a/bins/one": "12",
		"svc/teams/team-b/bins/one": "1234567",
		"svc/bins/operator":         "1",
		"svc/_meta/usage.json":      "unmeasured",
	} {
		f.put(t, key, body)
	}
	m, err := f.store.Measure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f.client.calls != 5 || len(m.Teams) != 2 || m.Teams["team-a"] != (teamblob.Tally{Bytes: 2, Objects: 1}) || m.Teams["team-b"] != (teamblob.Tally{Bytes: 7, Objects: 1}) || m.Operator != (teamblob.Tally{Bytes: 1, Objects: 1}) {
		t.Fatalf("measurement: %+v, calls=%d", m, f.client.calls)
	}
	for _, key := range []string{"svc/teams/team-a/bins/one", "svc/teams/team-b/bins/one", "svc/bins/operator"} {
		if _, err := f.raw.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
			t.Fatalf("measurement removed old object %s: %v", key, err)
		}
	}
}

func TestMeasureReportsPartialListings(t *testing.T) {
	f := newFixture(t, 0)
	f.put(t, "svc/teams/team-a/bins/one", "a")
	f.put(t, "svc/teams/team-b/bins/one", "bb")
	failure := errors.New("listing refused")
	f.client.failPrefix, f.client.failure = "svc/teams/team-a/", failure
	m, err := f.store.Measure(t.Context())
	if !errors.Is(err, failure) || len(m.Teams) != 1 || m.Teams["team-b"].Bytes != 2 {
		t.Fatalf("partial measurement: %+v, %v", m, err)
	}
	if _, ok := m.Teams["team-a"]; ok {
		t.Fatal("failed namespace received a complete tally")
	}
}

func TestMeasureReportsTruncatedListings(t *testing.T) {
	f := newFixture(t, 1)
	for i := range 1001 {
		f.put(t, fmt.Sprintf("svc/teams/team-a/bins/%04d", i), "x")
	}
	m, err := f.store.Measure(t.Context())
	if !errors.Is(err, teamblob.ErrListTruncated) || len(m.Teams) != 0 {
		t.Fatalf("truncated measurement: %+v, %v", m, err)
	}
}
