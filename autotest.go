// Autotest watches source code directories for changes and automatically runs
// ‘go test’; useful for continuous integration, test driven and behavior driven development workflows (CI, BDD, TDD)
package main

// autotest github.com/roastery/[paths...] [packages...] [testflags]
//  - new module for log colorization
//  - use StringArray (TestFlags, paths)

import (
	"fmt"
	"github.com/go-fsnotify/fsnotify"
	"go/build"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type watcher struct {
	// Finished is signaled when the watcher is closed.
	Finished chan bool

	// SettleTime indicates how long to wait after the last file system change before launching.
	SettleTime time.Duration

	// IgnoreDirs lists the names of directories that should not be watched for changes.
	IgnoreDirs map[string]bool

	// IgnoreFiles is a list of regular expression patterns for files that should be ignored.
	IgnoreFiles []*regexp.Regexp

	// TestFlags contains optional arguments for 'go test'.
	TestFlags []string

	debug       bool
	fs          *fsnotify.Watcher
	done        chan bool
	gosrc       string
	paths       []string
	timeSuccess time.Time
	timeFailure time.Time
	lastState   int
}

// Values for lastState
const (
	starting = iota
	working
	failing
)

// Go's time package does not provide a method such as:
//  func (d *Duration) Round(Duration)
// So we implement it here.
func round(duration, interval time.Duration) time.Duration {
	var t int64 = int64(duration) + int64(interval)/2
	return time.Duration(t - t%int64(interval))
}

func newWatcher() (*watcher, error) {
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	self := &watcher{
		Finished:   make(chan bool),
		SettleTime: 2 * time.Second,
		IgnoreDirs: map[string]bool{".git": true},
		IgnoreFiles: []*regexp.Regexp{
			regexp.MustCompile(`\..*\.swp$`),
		},
		TestFlags: make([]string, 0),
		debug:     false,
		fs:        fs,
		done:      make(chan bool),
		gosrc:     filepath.Join(os.Getenv("GOPATH"), "src"),
		paths:     make([]string, 0),
		lastState: starting,
	}
	return self, nil
}

func (self *watcher) Close() error {
	return self.fs.Close()
}

func (self *watcher) Start() {
	go self.monitorChanges()
}

func (self *watcher) Stop() {
	self.done <- true
}

func (self *watcher) Add(path string) error {
	// watch the file system path
	err := self.fs.Add(path)
	if err != nil {
		log.Fatal(err)
	}
	self.paths = append(self.paths, path)

	// is it a package dir (under $GOPATH/src?)
	if pkg := self.getPackageName(path); pkg != "" && self.debug {
		log.Println("package:", pkg, "in path:", path)
	}

	log.Println("watching for changes:", path)
	return err
}

func (self *watcher) Remove(path string) error {
	// find path in self.paths, remove the entry
	for i, val := range self.paths {
		if val == path {
			// delete entry at position i
			copy(self.paths[i:], self.paths[i+1:])
			self.paths = self.paths[0 : len(self.paths)-1]
			break
		}
	}
	return self.fs.Remove(path)
}

// AddRecursive walks a directory recursively, and watches all subdirectories.
func (self *watcher) AddRecursive(path string) error {
	return filepath.Walk(path, func(subpath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if _, ignore := self.IgnoreDirs[info.Name()]; ignore {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return self.Add(subpath)
		}
		return nil
	})
}

// RunTests invokes the 'go test' tool for all monitored packages.
func (self *watcher) RunTests() {
	if err := self.handleModifications(); err != nil {
		msg := "error: " + err.Error()
		if self.lastState != failing {
			self.timeFailure = time.Now()
		}
		if self.lastState == working {
			msg += fmt.Sprintf(" (%s success)", round(time.Since(self.timeSuccess), time.Second))
		}
		self.lastState = failing
		log.Println("\u001b[31m" + msg + "\u001b[0m")
	} else {
		msg := ""
		if self.lastState != working {
			self.timeSuccess = time.Now()
		}
		if self.lastState == failing {
			msg = fmt.Sprintf("success after %s failures", round(time.Since(self.timeFailure), time.Second))
		}
		self.lastState = working
		if len(msg) != 0 {
			log.Println("\u001b[32m" + msg + "\u001b[0m")
		}
	}
}

// monitorChanges is the main processing loop for file system notifications.
func (self *watcher) monitorChanges() {
	modified := false
	for {
		select {
		case <-self.done:
			self.Finished <- true
			return

		case err := <-self.fs.Errors:
			log.Println("error:", err)

		case event := <-self.fs.Events:
			mod, err := self.handleEvent(event)
			if err != nil {
				log.Println("error:", err)
			} else if mod {
				modified = true
			}

		case <-time.After(self.SettleTime):
			if modified {
				self.RunTests()
				modified = false
			}
		}
	}
}

// handleEvent handles a file system change notification.
func (self *watcher) handleEvent(event fsnotify.Event) (bool, error) {
	filename := event.Name
	modified := false

	if event.Op&fsnotify.Create != 0 {
		info, err := os.Stat(filename)
		if err != nil {
			// ENOENT can occur here for temp files (already deleted)
			if strings.HasSuffix(err.Error(), "no such file or directory") {
				return false, nil
			}
			return false, err
		}
		if info.IsDir() {
			if err := self.Add(filename); err != nil {
				return false, err
			}
		} else {
			if self.debug {
				log.Println("created:", filename)
			}
			modified = true
		}
	}
	if event.Op&fsnotify.Remove != 0 {
		if err := self.Remove(filename); err != nil {
			// "can't remove non-existent inotify watch" is OK
			if !strings.HasPrefix(err.Error(), "can't remove non-existent inotify watch") {
				return false, err
			}
		}
		if self.debug {
			log.Println("removed:", filename)
		}
		modified = true
	}
	if event.Op&fsnotify.Write != 0 {
		// skip file if it matches any regexp in IgnoreFiles
		skip := false
		base := filepath.Base(filename)
		for _, re := range self.IgnoreFiles {
			if re.MatchString(base) {
				skip = true
				break
			}
		}
		if skip {
			if self.debug {
				log.Println("skipping:", filename)
			}
		} else {
			if self.debug {
				log.Println("modified:", filename)
			}
			modified = true
		}
	}
	return modified, nil
}

// handleModifications launches 'go test'.
func (self *watcher) handleModifications() error {
	args := make([]string, 1+len(self.TestFlags))
	args[0] = "test"
	copy(args[1:], self.TestFlags)
	npkg := 0
	for _, path := range self.paths {
		if pkg := self.getPackageName(path); pkg != "" {
			args = append(args, pkg)
			npkg++
		}
	}
	cmd := exec.Command("go", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Printf("running go test with %d packages\n", npkg)
	return cmd.Run()
}

// getPackageName returns the go package name for a path, or "" if not a package dir.
func (self *watcher) getPackageName(path string) string {
	if pkg, err := filepath.Rel(self.gosrc, path); err == nil {
		return pkg
	}
	return ""
}

// --------------------------------------------------------------------------

func getCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	return cwd
}

// findPackage looks for path in the current directory, and any go source dirs,
// and returns the resolved path or an empty string if not found.
func findPackage(path string) string {
	// check relative to current directory first
	if stat, err := os.Stat(path); err == nil && stat.IsDir() {
		if !filepath.IsAbs(path) {
			path = filepath.Join(getCwd(), path)
		}
		return path
	}

	// check GOROOT / GOPATH
	for _, srcDir := range build.Default.SrcDirs() {
		pkg, err := build.Default.Import(path, srcDir, build.FindOnly)
		if err == nil {
			return pkg.Dir
		}
	}

	log.Println("package not found:", path)
	return ""
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" {
			fmt.Printf(`Monitors the file system and automatically runs 'go test' on changes.

usage: %s [-h | --help] [testflags] [path...] [package...]

options:
  -h, --help   print this message
  testflags    flags supported by 'go test'; see 'go help testflag'
  path...      filesystem path, monitored recursively
  package...   go package name for which 'go test' will be issued
`, os.Args[0])
			os.Exit(0)
		}
	}
	if os.Getenv("GOPATH") == "" {
		log.Fatalln("GOPATH is not set")
	}

	w, err := newWatcher()
	if err != nil {
		log.Fatal(err)
	}
	w.SettleTime = 500 * time.Millisecond

	// signals used to stop
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, os.Kill, syscall.SIGTERM)
	go func() {
		//signal := <-signals
		//log.Println("got signal:", signal)
		<-signals
		w.Stop()
	}()

	// monitor paths
	gotOne := false
	for _, arg := range os.Args[1:] {
		if arg[0] == '-' {
			w.TestFlags = append(w.TestFlags, arg)
		} else if path := findPackage(arg); path != "" {
			if err := w.AddRecursive(path); err != nil {
				log.Fatal(err)
			} else {
				gotOne = true
			}
		}
	}

	if !gotOne {
		log.Fatalln("no paths to watch")
	}

	w.Start()
	w.RunTests()
	<-w.Finished
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	log.Println("exiting")
}
