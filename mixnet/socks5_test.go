package mixnet

import "testing"

func TestEncodeAddrAndParse(t *testing.T) {
	if b, _ := EncodeAddr("mint.test"); b[0] != 3 || b[1] != 9 || string(b[2:]) != "mint.test" {
		t.Fatalf("domain: %v", b)
	}
	if b, _ := EncodeAddr("1.2.3.4"); len(b) != 5 || b[0] != 1 {
		t.Fatalf("ipv4: %v", b)
	}
	if b, _ := EncodeAddr("::1"); len(b) != 17 || b[0] != 4 {
		t.Fatalf("ipv6: %v", b)
	}
	if p, _ := ParseProxy("socks5h://127.0.0.1"); p != "127.0.0.1:1080" {
		t.Fatal(p)
	}
}
