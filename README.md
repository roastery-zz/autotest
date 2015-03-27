# autotest

Autotest watches source code directories for changes and automatically runs ‘go test’.

## Installation

This one-liner does the magic:
```
go get github.com/roastery/autotest
```

## Usage

```
usage: autotest [-h | --help] [testflags] [path...] [package...]
options:
  -h, --help   print this message
  testflags    flags supported by 'go test'; see 'go help testflag'
  path...      filesystem path, monitored recursively
  package...   go package name for which 'go test' will be issued
```

## Features

 * Automatically watches file system for changes.
 * Works with both file system paths, or Go package paths.
 * Looks for changes in the specified directory, and all subdirectories.
 * Keeps track of whether tests are succeeding or failing and provides a useful colored output in the terminal.
 * Supports additional test flags that are passed through to ‘go test’.

## Examples

### TDD/BDD workflow

For a [test-driven development](http://en.wikipedia.org/wiki/Test-driven_development) workflow, launch a command such as ```autotest github.com/roastery``` in a console and then keep working on your code and tests in your editor.

When you save, the tests will automatically run.

### Benchmarks

Use ```autotest -bench=. [package...]``` to automatically run your Go benchmarks.

## Contributions Welcome

Please star this repository if you find it useful.  Pull requests are also welcome.
