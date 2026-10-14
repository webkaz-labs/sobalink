package distribution

import (
	"errors"
	"path/filepath"
	"sort"
)

// routecat uses a zero-value tsd.System as its data-plane subsystem container.
// newLocoBackend, Server.startLocked, Client.initLocked and createEngine install
// no policy client. The separately pinned adapted engine's SetSubsystem calls
// also install only networking subsystems. tsd.PolicyClientOrDefault therefore
// retains policyclient.NoPolicyClient; its empty certificate subject makes
// controlclient.signRegisterRequest return before certstore.Open or signing.
//
// Pin every compiled file, not just the tsd importer: same-package code can
// access the private System without importing tsd itself. Any source change
// requires a fresh review of this no-policy/default-empty credential boundary.
// The existing dependency pins in darwinPolicyInputs remain independent.
var darwinRoutecatPolicyInputs = map[string]string{
	"disco.go":               "dc8c1e122ab92a046917289ec2dbffb9499cfbccda8bcebe251ff17f2a1d1a93",
	"listen.go":              "8de4853cfb91199686d1f3451fa929f67ad7a185440b28a5c941d2c384353ae9",
	"regions.go":             "36156c657a106fbe28d974557a6d8859ca779f4d3cc9b717aff7ccd54fab8ef6",
	"relay_failure.go":       "832a2b47a70d05aae1fbb72e6a97873c11f6c11784a527616233cba670c4e633",
	"relay_failure_other.go": "d5c96b35476ec68958671677a2f8bcb76eed14067ab9d00130bce492771b599a",
	"relay_resources.go":     "a5f5b30b450622575c5b31af55fb924762c1d9d26db5a99fa94da3a49b6abc6e",
	"tailcat.go":             "2b1220b8833f438cd3d84350dff4f19e0b62c37dabe06c5bf11470b55694b9fb",
	"underlay_policy.go":     "88c29c156ff423aa9ebbde6d709e909757a77c0bf576edc53b7bc0a3afc14621",
	"wan_candidates.go":      "9f9037784c9d80a9e9b7ec6675f91cd23d4545f02def89490c876676e8eb75f0",
	"wire.go":                "cb7d714cc51c77dd90ea0602109788742d94e9b96be37505a2bcf988aa78e31b",
}

func darwinRoutecatPolicyInventory(p goPackage) ([]Notice, error) {
	if p.ImportPath != routecatPackage || p.Module == nil || !p.Module.Main || p.Module.Path != Project || p.Module.Replace != nil || p.Module.Version != "" || p.Module.Sum != "" || !filepath.IsAbs(p.Module.Dir) || filepath.Clean(p.Dir) != filepath.Join(filepath.Clean(p.Module.Dir), filepath.FromSlash(routecatPath)) {
		return nil, errors.New("reviewed policy container is not from the expected main-module directory")
	}
	for _, inputs := range [][]string{p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles, p.CgoCFLAGS, p.CgoCPPFLAGS, p.CgoCXXFLAGS, p.CgoFFLAGS, p.CgoLDFLAGS, p.CgoPkgConfig} {
		if len(inputs) != 0 {
			return nil, errors.New("unreviewed adapted policy-container build inputs")
		}
	}
	if len(p.GoFiles) != len(darwinRoutecatPolicyInputs) {
		return nil, errors.New("adapted policy-container source set differs from review")
	}
	seen := map[string]bool{}
	var inputs []Notice
	for _, name := range p.GoFiles {
		want, ok := darwinRoutecatPolicyInputs[name]
		if !ok || seen[name] {
			return nil, errors.New("adapted policy-container source set differs from review")
		}
		seen[name] = true
		// Read from the module root so interior directory links are rejected.
		data, err := readSourceFile(p.Module.Dir, routecatPath+"/"+name)
		if err != nil || bytesHash(data) != want {
			return nil, errors.New("adapted policy-container source differs from review")
		}
		inputs = append(inputs, Notice{routecatPackage + "/" + name, want})
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	return inputs, nil
}
