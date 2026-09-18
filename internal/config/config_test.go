package config

import "testing"

func TestValidateTCPAddr(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{
			name:    "port only",
			addr:    ":8080",
			wantErr: false,
		},
		{
			name:    "ipv4",
			addr:    "127.0.0.1:8080",
			wantErr: false,
		},
		{
			name:    "ipv6",
			addr:    "[::1]:8080",
			wantErr: false,
		},
		{
			name:    "ephemeral port",
			addr:    ":0",
			wantErr: false,
		},
		{
			name:    "named port",
			addr:    ":http",
			wantErr: true,
		},
		{
			name:    "missing port",
			addr:    "localhost",
			wantErr: true,
		},
		{
			name:    "non numeric port",
			addr:    ":abc",
			wantErr: true,
		},
		{
			name:    "negative port",
			addr:    ":-1",
			wantErr: true,
		},
		{
			name:    "port too large",
			addr:    ":65536",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTCPAddr(tc.addr)

			if (err != nil) != tc.wantErr {
				t.Fatalf(
					"validateTCPAddr(%q) error = %v, wantError = %v",
					tc.addr,
					err,
					tc.wantErr,
				)
			}
		})
	}
}
