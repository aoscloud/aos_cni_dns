package main

import (
	"reflect"
	"strings"
	"testing"
)

func Test_generateDNSMasqConfig(t *testing.T) {
	testResult := strings.ReplaceAll(`## WARNING: THIS IS AN AUTOGENERATED FILE
## AND SHOULD NOT BE EDITED MANUALLY AS IT
## LIKELY TO AUTOMATICALLY BE REPLACED.
strict-order
local=/foobar.org/
domain=foobar.org
expand-hosts
pid-file=%{path}/cni0/pidfile
except-interface=lo
bind-dynamic
no-hosts
interface=cni0
addn-hosts=%{path}/cni0/addnhosts
conf-file=%{path}/cni0/localservers.conf
`, "%{path}", dnsNameConfPath())

	testConfig := dnsNameFile{
		AddOnHostsFile:       makePath("cni0", hostsFileName),
		Binary:               "/usr/bin/foo",
		ConfigFile:           makePath("cni0", confFileName),
		Domain:               "foobar.org",
		NetworkInterface:     "cni0",
		PidFile:              makePath("cni0", pidFileName),
		LocalServersConfFile: makePath("cni0", localServersConfFileName),
	}
	type args struct {
		config dnsNameFile
	}
	tests := []struct {
		name    string
		args    args
		want    []byte
		wantErr bool
	}{
		{"pass", args{testConfig}, []byte(testResult), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := generateDNSMasqConfig(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("generateDNSMasqConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("generateDNSMasqConfig() got = '%v', want '%v'", string(got), string(tt.want))
			}
		})
	}
}
