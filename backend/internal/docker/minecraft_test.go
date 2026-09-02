package docker

import (
	"slices"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"2G", 2 << 30, false},
		{"512M", 512 << 20, false},
		{"1024K", 1024 << 10, false},
		{"4g", 4 << 30, false},
		{" 8G ", 8 << 30, false},
		{"1073741824", 1073741824, false},
		{"", 0, true},
		{"lots", 0, true},
	}

	for _, tc := range tests {
		got, err := parseSize(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseSize(%q) = %d, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSize(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDeriveMemoryLimitAddsHeadroom(t *testing.T) {
	// A 2G heap must not become a 2G container limit, or the JVM's off-heap
	// allocations get the process OOM-killed mid-save.
	got := deriveMemoryLimit("2G")
	want := int64(2<<30) + (1 << 30)
	if got != want {
		t.Errorf("deriveMemoryLimit(2G) = %d, want %d", got, want)
	}

	if got := deriveMemoryLimit("nonsense"); got != 0 {
		t.Errorf("deriveMemoryLimit(nonsense) = %d, want 0 (uncapped)", got)
	}
}

// envMap turns the KEY=VALUE slice back into a map for assertions.
func envMap(t *testing.T, spec Spec) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kv := range buildEnv(spec) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		out[k] = v
	}
	return out
}

func TestBuildEnvVanilla(t *testing.T) {
	env := envMap(t, Spec{Type: "VANILLA", Version: "1.21.1", Memory: "2G"})

	if env["EULA"] != "TRUE" {
		t.Error("EULA must be TRUE or the image refuses to boot")
	}
	if env["TYPE"] != "VANILLA" {
		t.Errorf("TYPE = %q, want VANILLA", env["TYPE"])
	}
	if env["VERSION"] != "1.21.1" {
		t.Errorf("VERSION = %q, want 1.21.1", env["VERSION"])
	}
}

func TestBuildEnvModrinthOverridesType(t *testing.T) {
	env := envMap(t, Spec{
		Type: "FABRIC", Version: "1.21.1", Memory: "4G",
		ModrinthModpack: "cobblemon-official",
	})

	if env["TYPE"] != "MODRINTH" {
		t.Errorf("TYPE = %q, want MODRINTH", env["TYPE"])
	}
	if env["MODRINTH_MODPACK"] != "cobblemon-official" {
		t.Errorf("MODRINTH_MODPACK = %q", env["MODRINTH_MODPACK"])
	}
	// The pack pins its own Minecraft version; a stray VERSION fights with it.
	if _, ok := env["VERSION"]; ok {
		t.Error("VERSION must be dropped when a modpack pins the version")
	}
}

func TestBuildEnvCurseForgeOverridesType(t *testing.T) {
	env := envMap(t, Spec{
		Type: "FORGE", Memory: "6G",
		CurseForgeSlug: "all-the-mods-10", CurseForgeAPIKey: "key123",
	})

	if env["TYPE"] != "AUTO_CURSEFORGE" {
		t.Errorf("TYPE = %q, want AUTO_CURSEFORGE", env["TYPE"])
	}
	if env["CF_SLUG"] != "all-the-mods-10" || env["CF_API_KEY"] != "key123" {
		t.Errorf("CurseForge env not passed through: %v", env)
	}
}

func TestBuildEnvExtraEnvWins(t *testing.T) {
	env := envMap(t, Spec{
		Type: "VANILLA", Version: "1.21.1", Memory: "2G",
		ExtraEnv: map[string]string{"VERSION": "1.20.4", "DIFFICULTY": "hard"},
	})

	if env["VERSION"] != "1.20.4" {
		t.Errorf("ExtraEnv must override computed values, got VERSION=%q", env["VERSION"])
	}
	if env["DIFFICULTY"] != "hard" {
		t.Errorf("DIFFICULTY = %q, want hard", env["DIFFICULTY"])
	}
}

func TestBuildEnvSkipsEmptyValues(t *testing.T) {
	// An empty MEMORY would set the JVM heap to nothing and fail obscurely.
	got := buildEnv(Spec{Type: "VANILLA", Version: "1.21.1", Memory: ""})
	if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "MEMORY=") }) {
		t.Errorf("empty MEMORY should be omitted, got %v", got)
	}
}

func TestBuildResourcesCPUToNanoCPUs(t *testing.T) {
	res := buildResources(Spec{Memory: "2G", CPULimit: 2.5})
	if res.NanoCPUs != 2_500_000_000 {
		t.Errorf("NanoCPUs = %d, want 2500000000", res.NanoCPUs)
	}

	// Zero means uncapped, not "zero CPU".
	res = buildResources(Spec{Memory: "2G", CPULimit: 0})
	if res.NanoCPUs != 0 {
		t.Errorf("NanoCPUs = %d, want 0 for an uncapped server", res.NanoCPUs)
	}
}
