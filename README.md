# Package xz

This Go language package supports the reading and writing of xz
compressed streams. It includes also a gxz command for compressing and
decompressing data. The package is completely written in Go and doesn't
have any dependency on any C code.

The package is currently under development and APIs are subject to change. Single-threaded
decompression performance has been optimized using register caching and manual inlining.
Additionally, the package supports multi-threaded parallel block decompression and
provides APIs for block-level random access.

## Using the API

The following example program shows how to use the API.

```go
package main

import (
    "bytes"
    "io"
    "log"
    "os"

    "github.com/unxed/xz"
)

func main() {
    const text = "The quick brown fox jumps over the lazy dog.\n"
    var buf bytes.Buffer
    // compress text
    w, err := xz.NewWriter(&buf)
    if err != nil {
        log.Fatalf("xz.NewWriter error %s", err)
    }
    if _, err := io.WriteString(w, text); err != nil {
        log.Fatalf("WriteString error %s", err)
    }
    if err := w.Close(); err != nil {
        log.Fatalf("w.Close error %s", err)
    }
    // decompress buffer and write output to stdout
    r, err := xz.NewReader(&buf)
    if err != nil {
        log.Fatalf("NewReader error %s", err)
    }
    if _, err = io.Copy(os.Stdout, r); err != nil {
        log.Fatalf("io.Copy error %s", err)
    }
}
```

## Parallel Decompression

For streams containing multiple independent blocks, the package supports concurrent block decompression using a worker pool. This utilizes multiple CPU cores to improve decoding throughput.

To use the parallel reader, the input stream must implement `io.ReaderAt` to facilitate concurrent offset-based reads.

```go
package main

import (
    "bytes"
    "io"
    "log"
    "os"

    "github.com/unxed/xz"
)

func main() {
    // Read compressed data (must support io.ReaderAt)
    data, err := os.ReadFile("example.xz")
    if err != nil {
        log.Fatal(err)
    }
    rAt := bytes.NewReader(data)

    // Create parallel reader
    cfg := xz.ReaderConfig{}
    pr, err := cfg.NewParallelReader(rAt, int64(len(data)))
    if err != nil {
        log.Fatalf("NewParallelReader error: %s", err)
    }
    defer pr.Close()

    // Read decompressed output
    if _, err = io.Copy(os.Stdout, pr); err != nil {
        log.Fatalf("io.Copy error: %s", err)
    }
}
```

## Block-Level Random Access

The XZ backward-linked indexes can be parsed to extract block boundaries, enabling direct decompression of individual blocks without sequential reading of the entire file.

```go
// Parse block boundaries from seekable reader
blocks, err := xz.ParseBlocks(readerAt, size)
if err != nil {
    log.Fatal(err)
}

// Decompress individual blocks independently
for _, block := range blocks {
    // Seek to block start
    _, err := readerAt.Seek(block.Offset, io.SeekStart)
    if err != nil {
        log.Fatal(err)
    }

    // Initialize standalone block reader
    br, err := xz.ReaderConfig{}.NewBlockReader(readerAt, block.StreamFlags)
    if err != nil {
        log.Fatal(err)
    }

    // Decompress only this block
    if _, err = io.Copy(os.Stdout, br); err != nil {
        log.Fatal(err)
    }
}
```
## Documentation

You can find the full documentation at [pkg.go.dev](https://pkg.go.dev/github.com/unxed/xz).

## Using the gxz compression tool

The package includes a gxz command line utility for compression and
decompression.

Use following command for installation:

    $ go get github.com/unxed/xz/cmd/gxz

To test it call the following command.

    $ gxz bigfile

After some time a much smaller file bigfile.xz will replace bigfile.
To decompress it use the following command.

    $ gxz -d bigfile.xz

## Security & Vulnerabilities

The security policy is documented in [SECURITY.md](SECURITY.md). 

The software is not affected by the supply chain attack on the original xz
implementation, [CVE-2024-3094](https://nvd.nist.gov/vuln/detail/CVE-2024-3094).
This implementation doesn't share any files with the original xz implementation
and no patches or pull requests are accepted without a review.

All security advisories for this project are published under
[github.com/unxed/xz/security/advisories](https://github.com/unxed/xz/security/advisories?state=published).
