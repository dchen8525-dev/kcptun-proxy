// Package config owns the one strict whitelist shared by kcptun-server and
// kcptun-client. Unknown knobs fail loudly. The retired table covers both the
// spellings FCP removed and kcptun's own flag names, so copying a kcptun
// tutorial command line produces an actionable error instead of silently
// falling back to defaults.
package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	Product = "kcptun-proxy"

	// 4462 (not 4460/4461) so this tunnel can run beside FCP C++ and FCP Node
	// on the same VPS without stealing their port.
	DefaultPort       = 4462
	DefaultServerBind = "0.0.0.0:4462"
	DefaultClientBind = "127.0.0.1:1080"

	// Datagram bytes INCLUDING the 20B IPv4 + 8B UDP headers. kcp-go's
	// SetMtu() wants the UDP payload, so KCPMTU() subtracts OverheadIPUDP.
	DefaultMTU    = 1400
	OverheadIPUDP = 28

	DefaultDataShards = 10
	DefaultParity     = 3
	DefaultSeconds    = 8
	DefaultLogLevel   = "info"
)

// Version is a var so build.sh can inject the release tag with -ldflags -X.
// -h prints it on the first line; there is deliberately no --version flag.
var Version = "0.1.0"

// Config is the parsed, validated result. Nothing in here is a "maybe": every
// field is either an explicit user value or a documented constant.
type Config struct {
	Key          []byte
	Bind         string
	Peer         string
	DataShards   int
	ParityShards int
	MTU          int
	LogLevel     string
	LogFile      string
	Trace        string
	Speedtest    bool
	Seconds      int
	Help         bool
}

// KCPMTU converts the on-wire datagram budget to kcp-go's SetMtu argument.
func (c *Config) KCPMTU() int { return c.MTU - OverheadIPUDP }

func (c *Config) FECDisabled() bool { return c.ParityShards == 0 }

var valueOpts = map[string]bool{
	"key": true, "bind": true, "peer": true, "fec": true, "mtu": true,
	"log-level": true, "log-file": true, "trace": true, "seconds": true,
}

var boolOpts = map[string]bool{
	"help": true, "speedtest": true,
}

var aliases = map[string]string{
	"b": "bind",
	"h": "help",
}

type retired struct{ use, why string }

// A retired knob is an ERROR, never a warning. Silently ignoring "-mode fast3"
// would leave the user believing KCP was tuned when it is in fact pinned to the
// fixed fast3 profile below; silently ignoring "-mtu 1400" would leave them
// believing a packet budget they did not get.
var retiredKnobs = map[string]retired{
	// FCP's retired spellings (kept so a copy-pasted FCP command fails loudly).
	"host":              {"--peer=IP[:P]", "the remote address has exactly one name"},
	"H":                 {"--peer=IP[:P]", "-H was the old short spelling of --host"},
	"listen":            {"-b/--bind=IP[:P]", "the local address is the same concept on both ends"},
	"l":                 {"-b/--bind=IP[:P]", "-l was the old short spelling of --listen"},
	"local-port":        {"-b/--bind=IP:P", "one bind address already carries its port"},
	"port":              {"-b/--bind=IP:P or --peer=IP:P", "a second port flag could contradict the address"},
	"p":                 {"-b/--bind=IP:P or --peer=IP:P", "-p was the old short spelling of --port"},
	"mtu-min":           {"--mtu=N", "kcp-go has no PMTUD; this build uses one fixed datagram budget"},
	"mtu-max":           {"--mtu=N", "kcp-go has no PMTUD; this build uses one fixed datagram budget"},
	"no-pmtud":          {"nothing: PMTUD is not implemented here", "the flag describes a choice that does not exist"},
	"pmtud":             {"nothing: PMTUD is not implemented here", "the flag describes a choice that does not exist"},
	"packet-size":       {"--mtu=N", "the old fixed cap is now the only budget"},
	"m":                 {"--mtu=N", "-m was the old short spelling of --mtu in FCP"},
	"parity":            {"--fec=D:P", "parity is the P half of one geometry value"},
	"k":                 {"--fec=D:P", "there is no adaptive parity controller to pin"},
	"fec-budget":        {"--fec=D:P", "there is no adaptive FEC controller in kcp-go"},
	"log":               {"--log-level=LEVEL", "the short name hid the difference between a level and a file"},
	"version":           {"-h (the first line prints the version)", "one way to print the version is enough"},
	"key-file":          {"--key=HEX32", "one inline value, no path to resolve"},
	"psk-file":          {"--key=HEX32", "one inline value, no path to resolve"},
	"key-env":           {"--key=HEX32", "one inline value, no environment lookup"},
	"speedtest-seconds": {"--seconds=N", "one name for one duration"},
	// kcptun's own flags. They are all fixed internally (see InternalProfile),
	// except the three that genuinely map onto this whitelist.
	"localaddr":   {"-b/--bind=IP[:P]", "kcptun's local listen address"},
	"remoteaddr":  {"--peer=IP[:P] (client) or -b/--bind=IP[:P] (server)", "kcptun's remote address"},
	"r":           {"--peer=IP[:P]", "-r was kcptun's short --remoteaddr"},
	"c":           {"nothing: config files are not supported", "a second source of truth for the same knobs"},
	"config":      {"nothing: config files are not supported", "a second source of truth for the same knobs"},
	"crypt":       {"nothing: AES-128-GCM is always on", "an unencrypted tunnel is not a supported mode"},
	"mode":        {"nothing: the fast3 profile is fixed", "see docs/InternalProfile.md"},
	"nodelay":     {"nothing: nodelay=1 is fixed", "see docs/InternalProfile.md"},
	"interval":    {"nothing: interval=10ms is fixed", "see docs/InternalProfile.md"},
	"resend":      {"nothing: resend=2 is fixed", "see docs/InternalProfile.md"},
	"nc":          {"nothing: nc=1 is fixed", "see docs/InternalProfile.md"},
	"sndwnd":      {"nothing: 1024 is fixed", "see docs/InternalProfile.md"},
	"rcvwnd":      {"nothing: 1024 is fixed", "see docs/InternalProfile.md"},
	"dscp":        {"nothing: DSCP 46 is fixed", "see docs/InternalProfile.md"},
	"nocomp":      {"nothing: compression is never enabled", "kcp-go v5 has no built-in compression"},
	"sockbuf":     {"nothing: 4 MiB buffers are fixed", "see docs/InternalProfile.md"},
	"smuxver":     {"nothing: smux v2 is fixed", "both ends of this build always agree"},
	"smuxbuf":     {"nothing: 16 MiB is fixed", "see docs/InternalProfile.md"},
	"streambuf":   {"nothing: 4 MiB is fixed", "see docs/InternalProfile.md"},
	"datashard":   {"--fec=D:P", "D is the first half of the one FEC value"},
	"parityshard": {"--fec=D:P", "P is the second half of the one FEC value"},
	"quiet":       {"--log-level=off", "one log level scale, not a second boolean"},
	"tcp":         {"nothing: this build is UDP-only", "kcptun's TCP mode is a different transport"},
	"snmp":        {"nothing: SNMP is not compiled in", "use --trace for connection events"},
	"pprof":       {"nothing: pprof is not exposed", "no debug HTTP listener is started"},
	"autoexpire":  {"nothing: streams close with their TCP peer", "no idle reaper is needed"},
	"acknodelay":  {"nothing: ACK-no-delay is fixed on", "see docs/InternalProfile.md"},
	"compression": {"nothing: compression is never enabled", "kcp-go v5 has no built-in compression"},
	"scavengettl": {"nothing: not implemented", "see docs/InternalProfile.md"},
	"keepalive":   {"nothing: smux keepalive is fixed at 10s/30s", "see docs/InternalProfile.md"},
}

// Parse implements the whitelist. Every value option MUST be written with '=';
// accepting a space-separated value is how "--no-pmtud 3" used to silently
// swallow the 3 and shift every later argument.
func Parse(role string, argv []string) (*Config, error) {
	c := &Config{
		DataShards:   DefaultDataShards,
		ParityShards: DefaultParity,
		MTU:          DefaultMTU,
		LogLevel:     DefaultLogLevel,
		Seconds:      DefaultSeconds,
	}
	if role == "server" {
		c.Bind = DefaultServerBind
	} else {
		c.Bind = DefaultClientBind
	}

	raw := map[string]string{}
	for _, arg := range argv {
		if !strings.HasPrefix(arg, "-") {
			return nil, fmt.Errorf("unexpected argument %q: %s already IS the %s role, it takes no subcommand", arg, exeName(role), role)
		}
		name, val, hasVal, err := splitOpt(arg)
		if err != nil {
			return nil, err
		}
		canon := name
		if a, ok := aliases[name]; ok {
			canon = a
		}
		if r, ok := retiredKnobs[name]; ok {
			return nil, fmt.Errorf("%s is no longer accepted; use %s (%s)", display(name, arg), r.use, r.why)
		}
		if boolOpts[canon] {
			if hasVal {
				return nil, fmt.Errorf("%s is a switch and takes no value; write it bare", display(canon, arg))
			}
			switch canon {
			case "help":
				c.Help = true
			case "speedtest":
				c.Speedtest = true
			}
			continue
		}
		if valueOpts[canon] {
			if !hasVal {
				return nil, fmt.Errorf("%s requires '=VALUE' (space-separated values are rejected on purpose)", display(canon, arg))
			}
			raw[canon] = val
			continue
		}
		return nil, fmt.Errorf("unknown option %s; run -h for the whitelist", display(name, arg))
	}

	if c.Help {
		// Help must work with no key and no peer.
		return c, nil
	}
	if err := apply(c, role, raw); err != nil {
		return nil, err
	}
	return c, nil
}

func apply(c *Config, role string, raw map[string]string) error {
	if v, ok := raw["key"]; ok {
		b, err := hex.DecodeString(v)
		if err != nil || len(v) != 32 {
			return fmt.Errorf("--key must be exactly 32 hex chars (16 bytes), got %q", v)
		}
		c.Key = b
	} else if !c.Speedtest {
		return fmt.Errorf("--key=HEX32 is required (32 hex chars, identical on both ends); speedtest generates one")
	}

	if v, ok := raw["bind"]; ok {
		defHost, defPort := defaultBindHostPort(role)
		a, err := NormalizeAddr(v, defHost, defPort)
		if err != nil {
			return fmt.Errorf("--bind: %w", err)
		}
		c.Bind = a
	}

	if v, ok := raw["peer"]; ok {
		if role == "server" {
			return fmt.Errorf("--peer is client-only; the server binds with -b/--bind")
		}
		a, err := NormalizeAddr(v, "", DefaultPort)
		if err != nil {
			return fmt.Errorf("--peer: %w", err)
		}
		c.Peer = a
	} else if role == "client" && !c.Speedtest {
		return fmt.Errorf("--peer=IP[:P] is required for client (port defaults to %d)", DefaultPort)
	}

	if v, ok := raw["fec"]; ok {
		d, p, err := parseFEC(v)
		if err != nil {
			return fmt.Errorf("--fec: %w", err)
		}
		c.DataShards, c.ParityShards = d, p
	}

	if v, ok := raw["mtu"]; ok {
		n, err := parseInt(v, 576, 9000)
		if err != nil {
			return fmt.Errorf("--mtu: %w", err)
		}
		c.MTU = n
	}

	if v, ok := raw["log-level"]; ok {
		if !validLogLevel(v) {
			return fmt.Errorf("--log-level: %q is not one of debug|info|warn|error|off", v)
		}
		c.LogLevel = v
	}
	if v, ok := raw["log-file"]; ok {
		if v == "" {
			return fmt.Errorf("--log-file needs a non-empty path")
		}
		c.LogFile = v
	}
	if v, ok := raw["trace"]; ok {
		if v == "" {
			return fmt.Errorf("--trace needs a non-empty prefix")
		}
		c.Trace = v
	}
	if v, ok := raw["seconds"]; ok {
		n, err := parseInt(v, 3, 30)
		if err != nil {
			return fmt.Errorf("--seconds: %w", err)
		}
		c.Seconds = n
	}

	if role == "server" && c.Speedtest {
		return fmt.Errorf("--speedtest is client-only (it runs an in-process loopback)")
	}
	if c.Speedtest {
		// A loopback test needs no peer and no preset key; main() will make one.
		c.Peer = ""
	}
	return nil
}

func splitOpt(arg string) (name, val string, hasVal bool, err error) {
	body := arg[1:]
	if strings.HasPrefix(arg, "--") {
		body = arg[2:]
	}
	if body == "" {
		return "", "", false, fmt.Errorf("bare %q is not an option", arg)
	}
	if i := strings.IndexByte(body, '='); i >= 0 {
		return body[:i], body[i+1:], true, nil
	}
	return body, "", false, nil
}

func display(name, arg string) string {
	if strings.HasPrefix(arg, "--") || len(name) > 1 {
		return "--" + name
	}
	return "-" + name
}

func exeName(role string) string { return "kcptun-" + role }

func defaultBindHostPort(role string) (string, int) {
	if role == "server" {
		return "0.0.0.0", DefaultPort
	}
	return "127.0.0.1", 1080
}

func validLogLevel(s string) bool {
	switch s {
	case "debug", "info", "warn", "error", "off":
		return true
	}
	return false
}

func parseInt(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", s)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%d is out of range %d..%d", n, lo, hi)
	}
	return n, nil
}

func parseFEC(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%q is not D:P (e.g. 10:3)", s)
	}
	d, err := parseInt(parts[0], 1, 255)
	if err != nil {
		return 0, 0, fmt.Errorf("bad data shard count %q: %w", parts[0], err)
	}
	p, err := parseInt(parts[1], 0, 255)
	if err != nil {
		return 0, 0, fmt.Errorf("bad parity shard count %q: %w", parts[1], err)
	}
	if d+p > 256 {
		return 0, 0, fmt.Errorf("D+P=%d exceeds kcp-go's 256-shard limit", d+p)
	}
	return d, p, nil
}

// NormalizeAddr adds the default port when it is missing. IPv6 literals are
// bracketed. Unlike the C++/Node FCP parsers, Go's net package handles
// "[::1]:4462" correctly -- see docs/参数对照.md for that delta.
func NormalizeAddr(s, defHost string, defPort int) (string, error) {
	if s == "" {
		if defHost == "" {
			return "", fmt.Errorf("empty address and no default host")
		}
		return net.JoinHostPort(defHost, strconv.Itoa(defPort)), nil
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return checkAddr(s)
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return checkAddr(net.JoinHostPort(s[1:len(s)-1], strconv.Itoa(defPort)))
	}
	if ip := net.ParseIP(s); ip != nil {
		return checkAddr(net.JoinHostPort(s, strconv.Itoa(defPort)))
	}
	if strings.Contains(s, ":") {
		return "", fmt.Errorf("%q: write host:port or [IPv6]:port", s)
	}
	return checkAddr(net.JoinHostPort(s, strconv.Itoa(defPort)))
}

func checkAddr(s string) (string, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("empty host in %q", s)
	}
	if strings.Contains(host, " ") {
		return "", fmt.Errorf("whitespace in host %q", host)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("bad port %q (want 1..65535)", portStr)
	}
	return s, nil
}

// Usage is printed by -h. The first line carries the version, matching FCP.
func Usage(role string) string {
	head := fmt.Sprintf("%s %s (kcptun-style KCP tunnel, Go)\n", Product, Version)
	if role == "server" {
		return head + serverUsage
	}
	return head + clientUsage
}

const commonUsage = `
Values MUST use --name=VALUE. Switches (--help --speedtest) take no value.
Unknown, renamed and retired options are hard errors; nothing falls back
silently. Numeric values are range-checked.

Key (required, both ends equal):
  --key=HEX32           16-byte pre-shared key, 32 hex chars. AES-128-GCM.
                        Identical string to FCP's --key, so the same key file
                        works for both tunnels.

Tuning:
  --fec=D:P             Reed-Solomon data:parity shards (default 10:3).
                        P=0 disables FEC entirely. Unlike FCP, P is a FIXED
                        value here: kcp-go has no loss-driven parity controller.
  --mtu=N               UDP datagram bytes INCLUDING 20B IPv4 + 8B UDP headers
                        (default 1400, range 576..9000). Internally SetMtu(N-28).
                        kcp-go has no PMTUD: this is one fixed budget, not a
                        probe floor. Keep both ends equal.

Logging:
  --log-level=LEVEL     debug|info|warn|error|off (default info); logs go to stderr
  --log-file=PATH       also append every log line to PATH (creates parent dirs)
  --trace=PREFIX        append one JSONL connection-event file per process:
                        <PREFIX>-<server|client>-<pid>.jsonl
                        (NOT FCP's controller trace; see docs/参数对照.md)
  -h, --help            print this help and exit

stats lines are written to stdout every 500ms and are NOT filtered by
--log-level, so a shell or GUI can parse them independently of the log stream.

Fixed internally (no flags): AES-128-GCM, KCP fast3 (nodelay=1 interval=10ms
resend=2 nc=1), sndwnd/rcvwnd=1024, DSCP 46, 4 MiB UDP buffers, smux v2
(16 MiB session / 4 MiB stream), keepalive 10s/30s. See docs/InternalProfile.md.

Differences from FCP: no PMTUD (--mtu replaces --mtu-min/--mtu-max/--no-pmtud),
no adaptive FEC, no epoch key roll, no SACK v2, no stateless reset. Passing any
kcptun flag (-mode, -crypt, --sndwnd, ...) is a hard error with a replacement.
`

const serverUsage = `Usage: kcptun-server [options]

  -b, --bind=IP[:P]     UDP listen address (default 0.0.0.0:4462)
                        For each accepted stream the server reads the target
                        address, dials it over TCP, and relays both directions.
` + commonUsage + `
Example:
  kcptun-server --key=00112233445566778899aabbccddeeff -b=0.0.0.0:4462 --fec=10:3
`

const clientUsage = `Usage: kcptun-client [options]

  --peer=IP[:P]         KCP server to connect to (required; port defaults to 4462)
  -b, --bind=IP[:P]     local proxy listen address (default 127.0.0.1:1080);
                        one port speaks SOCKS5, HTTP CONNECT and absolute-form
                        HTTP, exactly like FCP's local entry.
  --speedtest           run an in-process loopback throughput test; needs no
                        server, no --peer and no --key (a random key is made)
  --seconds=N           seconds for --speedtest, 3..30 (default 8)
` + commonUsage + `
Example:
  kcptun-client --key=00112233445566778899aabbccddeeff --peer=vps.example.com:4462 -b=127.0.0.1:1080
  curl -x socks5h://127.0.0.1:1080 https://example.com
`
