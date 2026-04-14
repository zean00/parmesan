package toolsecurity

import "testing"

func TestProviderURLPolicyValidate(t *testing.T) {
	policy := ProviderURLPolicy{AllowedHosts: []string{"tools.example.com"}}
	if err := policy.Validate("https://tools.example.com/mcp"); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if err := policy.Validate("http://tools.example.com/mcp"); err == nil {
		t.Fatal("Validate() error = nil, want insecure scheme rejection")
	}
	if err := policy.Validate("https://internal.example.net/mcp"); err == nil {
		t.Fatal("Validate() error = nil, want host allowlist rejection")
	}
}

func TestProviderURLPolicyValidateAllowsLocalDevOverride(t *testing.T) {
	policy := ProviderURLPolicy{AllowLocalDev: true}
	if err := policy.Validate("http://127.0.0.1:8080/mcp"); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
