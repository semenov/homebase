package detect

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/semenov/homebase/internal/proto"
)

type cargoManifest struct {
	Package *struct {
		Name string `toml:"name"`
	} `toml:"package"`
	Workspace *struct{} `toml:"workspace"`
	Bin       []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
}

func buildRust(dir, start string) (*Plan, error) {
	var m cargoManifest
	if _, err := toml.DecodeFile(filepath.Join(dir, "Cargo.toml"), &m); err != nil {
		return nil, proto.Errf(proto.CodeConfig, "", "invalid Cargo.toml: %v", err)
	}
	if m.Package == nil {
		return nil, proto.Errf(proto.CodeStackUnknown, "add a Dockerfile that builds the server crate of the workspace", "Cargo.toml is a workspace without a root package")
	}
	bin, customPath, err := rustBinary(dir, &m)
	if err != nil {
		return nil, err
	}

	// Build dependencies first against stub sources so that code-only changes
	// reuse the cached dependency layer. Skipped when the layout is unusual.
	deps := ""
	if !customPath && !exists(filepath.Join(dir, "build.rs")) {
		stubs := "mkdir -p src && echo 'fn main() {}' > src/main.rs"
		touch := "src/main.rs"
		if exists(filepath.Join(dir, "src/lib.rs")) {
			stubs += " && touch src/lib.rs"
			touch += " src/lib.rs"
		}
		deps = fmt.Sprintf("COPY Cargo.toml Cargo.lock* ./\nRUN %s && cargo build --release && rm -rf src\n", stubs)
		deps += "COPY . .\nRUN touch " + touch + " && cargo build --release --bin " + bin + "\n"
	} else {
		deps = "COPY . .\nRUN cargo build --release --bin " + bin + "\n"
	}

	cmd := `["/usr/local/bin/app"]`
	if start != "" {
		cmd = shellCMD(start)
	}
	return &Plan{Stack: "rust", Port: 8080, Generated: `FROM rust:1-bookworm AS build
WORKDIR /src
` + deps + `
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libssl3 && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/target/release/` + bin + ` /usr/local/bin/app
ENV PORT=8080
EXPOSE 8080
CMD ` + cmd + "\n"}, nil
}

// rustBinary picks the binary to run: the only [[bin]], the one named like the
// package, or the package itself (src/main.rs).
func rustBinary(dir string, m *cargoManifest) (name string, customPath bool, err error) {
	for _, b := range m.Bin {
		if b.Path != "" && b.Path != "src/main.rs" {
			customPath = true
		}
	}
	switch {
	case len(m.Bin) == 1 && m.Bin[0].Name != "":
		return m.Bin[0].Name, customPath, nil
	case len(m.Bin) > 1:
		var names []string
		for _, b := range m.Bin {
			if b.Name == m.Package.Name {
				return b.Name, customPath, nil
			}
			names = append(names, b.Name)
		}
		return "", false, proto.Errf(proto.CodeStackUnknown, "add a Dockerfile, or name the server binary like the package", "Cargo.toml has several binaries (%s)", strings.Join(names, ", "))
	case exists(filepath.Join(dir, "src/main.rs")):
		return m.Package.Name, customPath, nil
	}
	return "", false, proto.Errf(proto.CodeStackUnknown, "a web app needs src/main.rs (or a [[bin]] target)", "the Rust package %q has no binary", m.Package.Name)
}
