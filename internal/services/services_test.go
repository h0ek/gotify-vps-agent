package services

import "testing"

func TestProfilesAreUniqueAndSupported(t *testing.T) {
	seen := map[string]bool{}
	for _, profile := range Profiles() {
		if profile.ID == "" || profile.Name == "" {
			t.Fatalf("invalid profile: %#v", profile)
		}
		if seen[profile.ID] {
			t.Fatalf("duplicate profile ID %q", profile.ID)
		}
		seen[profile.ID] = true
		if len(profile.Candidates) == 0 && profile.UnitGlob == "" {
			t.Fatalf("profile %q has no unit candidates", profile.ID)
		}
	}
	for _, id := range []string{"ssh", "nginx", "php-fpm", "mariadb", "postgresql", "tor"} {
		if _, ok := ProfileByID(id); !ok {
			t.Fatalf("missing profile %q", id)
		}
	}
}
