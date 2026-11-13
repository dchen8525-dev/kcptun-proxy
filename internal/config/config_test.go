package config

import (
	"strings"
	"testing"
)

const testKey = "00112233445566778899aabbccddeeff"

func clientArgs(extra ...string) []string {
	return append([]string{"--key=" + testKey, "--peer=127.0.0.1:4462"}, extra...)
}

func serverArgs(extra ...string) []string {
	return append([]string{"--key=" + testKey}, extra...)
}

func TestDefaults(t *testing.T) {
	c, err := Parse("client", clientArgs())
	if err != nil {
		t.Fatal(err)
	}
	if c.Bind != "127.0.0.1:1080" || c.Peer != "127.0.0.1:4462" {
		t.Fatalf("bind/peer: %q %q", c.Bind, c.Peer)
	}
	if c.MTU != 1400 || c.KCPMTU() != 1372 {
		t.Fatalf("mtu=%d kcp=%d", c.MTU, c.KCPMTU())
	}
	if c.DataShards != 10 || c.ParityShards != 3 || c.FECDisabled() {
		t.Fatalf("fec %d:%d", c.DataShards, c.ParityShards)
	}
	if c.LogLevel != "info" || c.Seconds != 8 || len(c.Key) != 16 {
		t.Fatalf("defaults: %+v", c)
	}
	s, err := Parse("server", serverArgs())
	if err != nil {
		t.Fatal(err)
	}
	if s.Bind != "0.0.0.0:4462" {
		t.Fatalf("server bind %q", s.Bind)
	}
}

func TestWhitelistValues(t *testing.T) {
	c, err := Parse("client", []string{
		"--key=" + testKey, "--peer=vps.example.com", "-b=127.0.0.1:8080",
		"--fec=20:5", "--mtu=1200", "--log-level=debug", "--log-file=logs/x.log",
		"--trace=traces/run", "--seconds=12",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Peer != "vps.example.com:4462" || c.Bind != "127.0.0.1:8080" {
		t.Fatalf("addr: %q %q", c.Peer, c.Bind)
	}
	if c.DataShards != 20 || c.ParityShards != 5 || c.MTU != 1200 || c.KCPMTU() != 1172 {
		t.Fatalf("tuning: %+v", c)
	}
	if c.LogLevel != "debug" || c.LogFile != "logs/x.log" || c.Trace != "traces/run" || c.Seconds != 12 {
		t.Fatalf("misc: %+v", c)
	}
}

func TestRemovedKnobsFailLoud(t *testing.T) {
	knobs := []string{
		// FCP's retired spellings
		"--host=1.2.3.4", "-H=1.2.3.4", "--listen=127.0.0.1:1080", "-l=127.0.0.1:1080",
		"--local-port=1080", "--port=4462", "-p=4462", "--mtu-min=1040", "--mtu-max=1500",
		"--no-pmtud", "--pmtud", "--packet-size=1400", "-m=1400", "--parity=3", "--k=3",
		"--fec-budget=0.8", "--log=debug", "--version", "--key-file=k.txt",
		"--psk-file=k.txt", "--key-env=K", "--speedtest-seconds=9",
		// kcptun's own flags
		"--mode=fast3", "--crypt=aes", "--nodelay=1", "--interval=10", "--resend=2",
		"--nc=1", "--sndwnd=1024", "--rcvwnd=1024", "--dscp=46", "--nocomp",
		"--sockbuf=4194304", "--smuxver=2", "--smuxbuf=16777216", "--streambuf=2097152",
		"--datashard=10", "--parityshard=3", "--quiet", "--tcp", "--snmp", "--pprof",
		"--autoexpire=60", "--acknodelay", "--compression", "--scavengettl=600",
		"--keepalive=10", "--localaddr=:1080", "--remoteaddr=1.2.3.4:4462",
		"-r=1.2.3.4:4462", "-c=cfg.json", "--config=cfg.json",
	}
	for _, k := range knobs {
		t.Run(k, func(t *testing.T) {
			_, err := Parse("client", clientArgs(k))
			if err == nil {
				t.Fatalf("%s was silently accepted", k)
			}
			if !strings.Contains(err.Error(), "no longer accepted") {
				t.Fatalf("%s: unexpected error %v", k, err)
			}
		})
	}
}

func TestUnknownAndMalformedArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown", []string{"--typo=1"}, "unknown option"},
		{"space value", []string{"--fec", "10:3"}, "requires '=VALUE'"},
		{"bare dash", []string{"-"}, "bare"},
		{"positional", []string{"server"}, "no subcommand"},
		{"bool with value", []string{"--help=true"}, "takes no value"},
		{"key short hex", []string{"--key=abcd"}, "32 hex chars"},
		{"key non hex", []string{"--key=" + strings.Repeat("z", 32)}, "32 hex chars"},
		{"bad fec shape", []string{"--fec=10"}, "not D:P"},
		{"fec zero d", []string{"--fec=0:3"}, "out of range"},
		{"fec over 256", []string{"--fec=200:100"}, "256-shard"},
		{"mtu low", []string{"--mtu=100"}, "out of range"},
		{"mtu high", []string{"--mtu=99999"}, "out of range"},
		{"bad level", []string{"--log-level=verbose"}, "not one of"},
		{"bad seconds", []string{"--seconds=99"}, "out of range"},
		{"empty log file", []string{"--log-file="}, "non-empty"},
		{"bad addr", []string{"--peer=1.2.3.4:notaport"}, "bad port"},
		{"ambiguous addr", []string{"--peer=a:b:c"}, "write host:port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("client", clientArgs(tc.args...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRoleConstraints(t *testing.T) {
	if _, err := Parse("client", []string{"--key=" + testKey}); err == nil {
		t.Fatal("client without --peer accepted")
	}
	if _, err := Parse("server", []string{}); err == nil {
		t.Fatal("server without --key accepted")
	}
	if _, err := Parse("server", serverArgs("--peer=1.2.3.4:4462")); err == nil {
		t.Fatal("server accepted --peer")
	}
	if _, err := Parse("server", serverArgs("--speedtest")); err == nil {
		t.Fatal("server accepted --speedtest")
	}
	if _, err := Parse("client", []string{"--speedtest", "--seconds=3"}); err != nil {
		t.Fatalf("speedtest should need neither key nor peer: %v", err)
	}
	if c, err := Parse("client", []string{"--key=" + testKey, "--speedtest"}); err != nil || c.Peer != "" {
		t.Fatalf("speedtest peer should be cleared: %+v %v", c, err)
	}
}

func TestHelp(t *testing.T) {
	c, err := Parse("client", []string{"-h"})
	if err != nil || !c.Help {
		t.Fatalf("help: %+v %v", c, err)
	}
	if _, err := Parse("server", []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"server", "client"} {
		u := Usage(role)
		if !strings.HasPrefix(u, Product+" "+Version) {
			t.Fatalf("%s usage lacks version header", role)
		}
		for _, must := range []string{"--key=HEX32", "--fec=D:P", "--mtu=N", "--log-level=LEVEL", "-h, --help"} {
			if !strings.Contains(u, must) {
				t.Fatalf("%s usage lacks %s", role, must)
			}
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"1.2.3.4:9999", "1.2.3.4:9999"},
		{"vps.example.com", "vps.example.com:4462"},
		{"1.2.3.4", "1.2.3.4:4462"},
		{"[::1]:4462", "[::1]:4462"},
		{"[::1]", "[::1]:4462"},
		{"::1", "[::1]:4462"},
	}
	for _, tc := range cases {
		got, err := NormalizeAddr(tc.in, "", DefaultPort)
		if err != nil || got != tc.want {
			t.Fatalf("%q -> %q (want %q): %v", tc.in, got, tc.want, err)
		}
	}
	if _, err := NormalizeAddr("", "", DefaultPort); err == nil {
		t.Fatal("empty addr with no default host accepted")
	}
	if _, err := NormalizeAddr("1.2.3.4:0", "", DefaultPort); err == nil {
		t.Fatal("port 0 accepted")
	}
}

func TestFECDisabled(t *testing.T) {
	c, err := Parse("client", clientArgs("--fec=10:0"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.FECDisabled() || c.ParityShards != 0 {
		t.Fatalf("fec=10:0 should disable FEC: %+v", c)
	}
}

// TestUsageExamplesAreRunnable guards against the help text drifting away from
// the parser. A space-separated value in an example is a hard error here, so a
// stale example would hand the user a command line that cannot start.
func TestUsageExamplesAreRunnable(t *testing.T) {
	const marker = "Example:\n"
	cases := []struct {
		role  string
		usage string
	}{
		{"server", serverUsage},
		{"client", clientUsage},
	}
	for _, tc := range cases {
		i := strings.Index(tc.usage, marker)
		if i < 0 {
			t.Fatalf("%s usage has no Example section", tc.role)
		}
		line := strings.Split(strings.TrimSpace(tc.usage[i+len(marker):]), "\n")[0]
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("bad %s example line: %q", tc.role, line)
		}
		if _, err := Parse(tc.role, fields[1:]); err != nil {
			t.Fatalf("%s example is not runnable: %v\n%s", tc.role, err, line)
		}
	}
}
