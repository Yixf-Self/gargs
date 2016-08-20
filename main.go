package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"runtime"
	"strings"
	"text/template"

	"github.com/alexflint/go-arg"
	"github.com/brentp/gargs/process"
	"github.com/brentp/xopen"
)

// Version is the current version
const Version = "0.3.2"

// ExitCode is the highest exit code seen in any command
var ExitCode = 0

// Params are the user-specified command-line arguments
type Params struct {
	Procs           int    `arg:"-p,help:number of processes to use."`
	Nlines          int    `arg:"-n,help:number of lines to consume for each command. -s and -n are mutually exclusive."`
	Command         string `arg:"positional,required,help:command to execute."`
	Sep             string `arg:"-s,help:regular expression split line with to fill multiple template spots default is not to split. -s and -n are mutually exclusive."`
	Verbose         bool   `arg:"-v,help:print commands to stderr before they are executed."`
	ContinueOnError bool   `arg:"-c,--continue-on-error,help:report errors but don't stop the entire execution (which is the default)."`
	Ordered         bool   `arg:"-o,help:keep output in order of input at cost of reduced parallelization; default is to output in order of return."`
	DryRun          bool   `arg:"-d,--dry-run,help:print (but do not run) the commands"`
}

// hold the arguments for each call that fill the template.
type tmplArgs struct {
	Lines []string
	Xs    []string
}

func main() {
	args := Params{Procs: 1, Nlines: 1}
	p := arg.MustParse(&args)
	if args.Sep != "" && args.Nlines > 1 {
		p.Fail("must specify either sep (-s) or n-lines (-n), not both")
	}
	if !xopen.IsStdin() {
		fmt.Fprintln(os.Stderr, "ERROR: expecting input on STDIN")
		os.Exit(255)
	}
	runtime.GOMAXPROCS(args.Procs)
	run(args)
	os.Exit(ExitCode)
}

func check(e error) {
	if e != nil {
		log.Fatal(e)
	}
}

func handleCommand(args *Params, cmd string, ch chan string) {
	if args.Verbose {
		fmt.Fprintf(os.Stderr, "command: %s\n", cmd)
	}
	if args.DryRun {
		fmt.Fprintf(os.Stdout, "%s\n", cmd)
		return
	}
	ch <- cmd
}

func genCommands(args *Params, tmpl *template.Template) <-chan string {
	ch := make(chan string)
	var resep *regexp.Regexp
	if args.Sep != "" {
		resep = regexp.MustCompile(args.Sep)
	}
	rdr, err := xopen.Ropen("-")
	check(err)

	go func() {
		re := regexp.MustCompile(`\r?\n`)
		lines := make([]string, 0, args.Nlines)
		var buf bytes.Buffer
		for {
			buf.Reset()
			line, err := rdr.ReadString('\n')
			if err == nil || (err == io.EOF && len(line) > 0) {
				line = re.ReplaceAllString(line, "")
				if resep != nil {
					toks := resep.Split(line, -1)
					check(tmpl.Execute(&buf, &tmplArgs{Xs: toks, Lines: []string{line}}))
					handleCommand(args, buf.String(), ch)
				} else {
					lines = append(lines, line)
				}
			} else {
				if err == io.EOF {
					break
				}
				log.Fatal(err)
			}
			if len(lines) == args.Nlines {
				check(tmpl.Execute(&buf, &tmplArgs{Lines: lines, Xs: lines}))
				lines = lines[:0]
				handleCommand(args, buf.String(), ch)
			}
		}
		if len(lines) > 0 {
			check(tmpl.Execute(&buf, &tmplArgs{Lines: lines, Xs: lines}))
			handleCommand(args, buf.String(), ch)
		}
		close(ch)
	}()
	return ch
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func run(args Params) {

	tmpl := makeCommandTmpl(args.Command)
	cmds := genCommands(&args, tmpl)

	stdout := bufio.NewWriter(os.Stdout)
	defer stdout.Flush()

	cancel := make(chan bool)
	defer close(cancel)

	for p := range process.Runner(cmds, cancel) {
		if ex := p.ExitCode(); ex != 0 {
			ExitCode = max(ExitCode, ex)
			if !args.ContinueOnError {
				close(cancel)
				break
			}
		}
		io.Copy(stdout, p)
	}

}

func makeCommandTmpl(cmd string) *template.Template {
	v := strings.Replace(cmd, "{}", "{{index .Lines 0}}", -1)
	re := regexp.MustCompile(`({\d+})`)
	v = re.ReplaceAllStringFunc(v, func(match string) string {
		return "{{index .Xs " + match[1:len(match)-1] + "}}"
	})

	tmpl, err := template.New(v).Parse(v)
	check(err)
	return tmpl
}
