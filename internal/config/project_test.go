package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShipTomlMoves(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ShipFile), []byte("name = \"api\"\nserver = \"root@x\"\nvolumes = [\"/data\"]\nport = 3000\n"), 0o644)
	p, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || !p.FromShip || p.Name != "api" || p.Deploy.Server != "root@x" || p.Deploy.Port != 3000 || p.Port != 0 {
		t.Fatalf("got %+v / %+v", p, p.Deploy)
	}
	if FindProjectDir(filepath.Join(dir)) != dir {
		t.Error("ship.toml should mark a project")
	}
	p.Start = "npm run dev"
	if err := p.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ShipFile)); !os.IsNotExist(err) {
		t.Error("ship.toml should be gone")
	}
	b, _ := os.ReadFile(filepath.Join(dir, ProjectFile))
	if !strings.Contains(string(b), "[deploy]") || !strings.Contains(string(b), `server = "root@x"`) {
		t.Fatalf("homebase.toml:\n%s", b)
	}
	p, _ = LoadProject(dir)
	if p.FromShip || p.Deploy.Volumes[0] != "/data" || p.Start != "npm run dev" {
		t.Fatalf("reloaded: %+v", p)
	}
}

func TestShipNameDiffers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ProjectFile), []byte("name = \"web\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ShipFile), []byte("name = \"web-prod\"\n"), 0o644)
	p, _ := LoadProject(dir)
	if p.Name != "web" || p.Deploy.Name != "web-prod" {
		t.Fatalf("got %+v / %+v", p, p.Deploy)
	}
}

func TestEmptyDeployIsDropped(t *testing.T) {
	dir := t.TempDir()
	p := &Project{Name: "x", Deploy: &Deploy{}}
	p.Save(dir)
	b, _ := os.ReadFile(filepath.Join(dir, ProjectFile))
	if strings.Contains(string(b), "[deploy]") {
		t.Fatalf("homebase.toml:\n%s", b)
	}
}
