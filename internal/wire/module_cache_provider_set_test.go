package wire

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateProviderSetFromModuleCache covers provider sets declared in a
// dependency module that is resolved from the module cache, which is how CI
// builds a multi-module repository without a go.work file.
func TestGenerateProviderSetFromModuleCache(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := importBenchRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		name = filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const libMod = "module example.com/lib\n\ngo 1.25.0\n\nrequire github.com/goforj/wire v1.2.0\n"
	libFiles := map[string]string{
		"go.mod": libMod,
		"provider.go": `package lib
import "github.com/goforj/wire"
// Config is supplied by the consumer.
type Config struct{ Name string }
// Service is built from the set below.
type Service struct{ Config Config }
// NewService builds a Service.
func NewService(c Config) *Service { return &Service{Config: c} }
// Set is only resolvable from this package's syntax.
var Set = wire.NewSet(NewService)
`,
	}
	writeModuleProxy(t, filepath.Join(root, "proxy"), "example.com/lib", "v1.0.0", libFiles)
	write("app/go.mod", "module example.com/app\n\ngo 1.25.0\n\nrequire (\n\tgithub.com/goforj/wire v1.2.0\n\texample.com/lib v1.0.0\n)\n\nreplace github.com/goforj/wire => "+filepath.ToSlash(repo)+"\n")
	write("app/wire.go", `//go:build wireinject

package app
import ("github.com/goforj/wire"; "example.com/lib")
// Initialize uses a provider set from a module-cache dependency.
func Initialize(c lib.Config) *lib.Service { wire.Build(lib.Set); return nil }
`)
	env := append(os.Environ(),
		"GOWORK=off",
		"GOFLAGS=-mod=mod -modcacherw",
		"GOPROXY=file://"+filepath.ToSlash(filepath.Join(root, "proxy")),
		"GOSUMDB=off",
		"GOMODCACHE="+filepath.Join(root, "modcache"),
		"WIRE_CACHE_DIR="+filepath.Join(root, "cache"),
	)
	wd := filepath.Join(root, "app")
	download := exec.Command("go", "mod", "download", "example.com/lib")
	download.Dir, download.Env = wd, env
	if output, err := download.CombinedOutput(); err != nil {
		t.Fatalf("go mod download: %v\n%s", err, output)
	}
	gens, errs := Generate(context.Background(), wd, env, []string{"."}, &GenerateOptions{})
	if len(errs) != 0 {
		t.Fatalf("Generate: %v", errs)
	}
	if len(gens) != 1 {
		t.Fatalf("generated %d targets, want 1", len(gens))
	}
	if len(gens[0].Errs) != 0 {
		t.Fatalf("Generate %s: %v", gens[0].PkgPath, gens[0].Errs)
	}
	if !strings.Contains(string(gens[0].Content), "lib.NewService(c)") {
		t.Fatalf("generated code does not call the provider from the module-cache set:\n%s", gens[0].Content)
	}
}

// writeModuleProxy lays out a GOPROXY file tree serving one module version.
func writeModuleProxy(t *testing.T, proxy, modPath, version string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(proxy, filepath.FromSlash(modPath), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"list":            version + "\n",
		version + ".info": `{"Version":"` + version + `"}`,
		version + ".mod":  files["go.mod"],
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.Create(filepath.Join(dir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	for name, content := range files {
		w, err := zw.Create(modPath + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
