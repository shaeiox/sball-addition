package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// flagSet is a very small getopt-style parser. It supports the forms used
// throughout the CLI:
//
//	--name=value   --name value   --flag   --flag=true   -f   --  (end of flags)
//
// Every flag must be declared up front, which keeps typos from being silently
// ignored (an unknown flag is an error).
type flagSet struct {
	command    string
	args       []string
	bools      map[string]*bool
	strs       map[string]*string
	ints       map[string]*int
	aliases    map[string]string
	positional []string
	usage      []string
}

func newFlagSet(command string, args []string) *flagSet {
	return &flagSet{
		command: command,
		args:    args,
		bools:   map[string]*bool{},
		strs:    map[string]*string{},
		ints:    map[string]*int{},
		aliases: map[string]string{},
	}
}

// Bool declares a boolean flag.
func (f *flagSet) Bool(name string, def bool, usage string, aliases ...string) *bool {
	v := def
	f.bools[name] = &v
	f.usage = append(f.usage, usageLine(name, usage, aliases...))
	for _, a := range aliases {
		f.aliases[a] = name
	}
	return &v
}

// String declares a string flag.
func (f *flagSet) String(name, def, usage string, aliases ...string) *string {
	v := def
	f.strs[name] = &v
	f.usage = append(f.usage, usageLine(name, usage, aliases...))
	for _, a := range aliases {
		f.aliases[a] = name
	}
	return &v
}

// Int declares an integer flag.
func (f *flagSet) Int(name string, def int, usage string, aliases ...string) *int {
	v := def
	f.ints[name] = &v
	f.usage = append(f.usage, usageLine(name, usage, aliases...))
	for _, a := range aliases {
		f.aliases[a] = name
	}
	return &v
}

func usageLine(name, usage string, aliases ...string) string {
	names := "--" + name
	for _, a := range aliases {
		prefix := "--"
		if len(a) == 1 {
			prefix = "-"
		}
		names += ", " + prefix + a
	}
	return fmt.Sprintf("  %-24s %s", names, usage)
}

// Parse consumes the arguments.
func (f *flagSet) Parse() error {
	rest := f.args
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case arg == "--":
			f.positional = append(f.positional, rest[i+1:]...)
			return nil
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			f.positional = append(f.positional, arg)
		default:
			name := strings.TrimLeft(arg, "-")
			value := ""
			hasValue := false
			if idx := strings.Index(name, "="); idx >= 0 {
				value, name, hasValue = name[idx+1:], name[:idx], true
			}
			canonical := name
			if a, ok := f.aliases[name]; ok {
				canonical = a
			}

			switch {
			case f.bools[canonical] != nil:
				if hasValue {
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("--%s: %q is not a boolean", canonical, value)
					}
					*f.bools[canonical] = b
				} else {
					*f.bools[canonical] = true
				}
			case f.strs[canonical] != nil:
				if !hasValue {
					if i+1 >= len(rest) {
						return fmt.Errorf("--%s requires a value", canonical)
					}
					i++
					value = rest[i]
				}
				*f.strs[canonical] = value
			case f.ints[canonical] != nil:
				if !hasValue {
					if i+1 >= len(rest) {
						return fmt.Errorf("--%s requires a value", canonical)
					}
					i++
					value = rest[i]
				}
				n, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("--%s: %q is not a number", canonical, value)
				}
				*f.ints[canonical] = n
			default:
				return fmt.Errorf("unknown flag %s", arg)
			}
		}
	}
	return nil
}

// Positional returns the positional arguments (after --).
func (f *flagSet) Positional() []string { return f.positional }

// Arg returns the positional argument at index i, or "".
func (f *flagSet) Arg(i int) string {
	if i < len(f.positional) {
		return f.positional[i]
	}
	return ""
}

// Usage renders the flag help block.
func (f *flagSet) Usage() string {
	return strings.Join(f.usage, "\n")
}
