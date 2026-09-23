package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// readHostKey takes the key an ssh server presents and hangs up before
// authenticating, as ssh-keyscan does.
func TestReadHostKeyTakesTheKeyTheServerPresents(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: false, PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		t.Error("the scan tried to authenticate")
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _, _ = ssh.NewServerConn(conn, cfg)
	}()
	got, err := readHostKey(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("read host key: %v", err)
	}
	if ssh.FingerprintSHA256(got) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("host key = %s, want %s", ssh.FingerprintSHA256(got), ssh.FingerprintSHA256(signer.PublicKey()))
	}
}

// An owner names the host a credential is for, so the scan refuses one that
// reaches the controller's own network rather than probing it.
func TestScanHostKeyRefusesAnInwardHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.8", "169.254.169.254", "localhost"} {
		if _, err := scanHostKey(context.Background(), host, 22); err == nil {
			t.Errorf("scan of %s went ahead", host)
		}
	}
}
