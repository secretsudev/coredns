package reverse

import (
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/middleware"

	"github.com/mholt/caddy"
)

func init() {
	caddy.RegisterPlugin("reverse", caddy.Plugin{
		ServerType: "dns",
		Action:     setupReverse,
	})
}

func setupReverse(c *caddy.Controller) error {
	networks, fallThrough, err := reverseParse(c)
	if err != nil {
		return middleware.Error("reverse", err)
	}

	dnsserver.GetConfig(c).AddMiddleware(func(next middleware.Handler) middleware.Handler {
		return Reverse{Next: next, Networks: networks, Fallthrough: fallThrough}
	})

	return nil
}

func reverseParse(c *caddy.Controller) (nets networks, fall bool, err error) {

	// normalize zones, validation is almost done by dnsserver
	// TODO(miek): need sane helpers for these.
	zones := make([]string, len(c.ServerBlockKeys))
	wildcard := false

	for i, str := range c.ServerBlockKeys {
		zones[i] = middleware.Host(str).Normalize()
	}

	for c.Next() {
		if c.Val() == "reverse" {

			var cidrs []*net.IPNet

			// parse all networks
			for _, cidr := range c.RemainingArgs() {
				if cidr == "{" {
					break
				}
				_, ipnet, err := net.ParseCIDR(cidr)
				if err != nil {
					return nil, false, c.Errf("network needs to be CIDR formatted: %q\n", cidr)
				}
				cidrs = append(cidrs, ipnet)
			}
			if len(cidrs) == 0 {
				return nil, false, c.ArgErr()
			}

			// set defaults
			var (
				template = "ip-" + templateNameIP + ".{zone[1]}"
				ttl      = 60
			)
			for c.NextBlock() {
				switch c.Val() {
				case "hostname":
					if !c.NextArg() {
						return nil, false, c.ArgErr()
					}
					template = c.Val()

				case "ttl":
					if !c.NextArg() {
						return nil, false, c.ArgErr()
					}
					ttl, err = strconv.Atoi(c.Val())
					if err != nil {
						return nil, false, err
					}

				case "wildcard":
					wildcard = true

				case "fallthrough":
					fall = true

				default:
					return nil, false, c.ArgErr()
				}
			}

			// prepare template
			// replace {zone[index]} by the listen zone/domain of this config block
			for i, zone := range zones {
				// TODO: we should be smarter about actually replacing this. This works, but silently allows "zone[-1]"
				// for instance.
				template = strings.Replace(template, "{zone["+strconv.Itoa(i+1)+"]}", zone, 1)
			}
			if !strings.HasSuffix(template, ".") {
				template += "."
			}

			// extract zone from template
			templateZone := strings.SplitAfterN(template, ".", 2)
			if len(templateZone) != 2 || templateZone[1] == "" {
				return nil, false, c.Errf("cannot find domain in template '%v'", template)
			}

			// Create for each configured network in this stanza
			for _, ipnet := range cidrs {
				// precompile regex for hostname to ip matching
				regexIP := regexMatchV4
				if ipnet.IP.To4() == nil {
					regexIP = regexMatchV6
				}
				prefix := "^"
				if wildcard {
					prefix += ".*"
				}
				regex, err := regexp.Compile(
					prefix + strings.Replace( // inject ip regex into template
						regexp.QuoteMeta(template), // escape dots
						regexp.QuoteMeta(templateNameIP),
						regexIP,
						1) + "$")
				if err != nil {
					return nil, false, err
				}

				nets = append(nets, network{
					IPnet:        ipnet,
					Zone:         templateZone[1],
					Template:     template,
					RegexMatchIP: regex,
					TTL:          uint32(ttl),
				})
			}
		}
	}

	// sort by cidr
	sort.Sort(nets)
	return nets, fall, nil
}
