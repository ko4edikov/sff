package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
	"github.com/ko4edikov/sff/pkg/source"
)

// testLevels maps the accepted (case-insensitive) --test-level values to their
// canonical Metadata API names.
var testLevels = map[string]string{
	"notestrun":         mdapi.TestLevelNone,
	"runspecifiedtests": mdapi.TestLevelSpecified,
	"runlocaltests":     mdapi.TestLevelLocal,
	"runalltestsinorg":  mdapi.TestLevelAll,
}

func newDeployCmd() *cobra.Command {
	var sourceDir, manifest, projectDir, testLevel, apiVersion string
	var metadata, tests []string
	var checkOnly, dryRun, ignoreWarnings, ignoreErrors, metadataFormat, tooling bool
	var wait int
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy source-format metadata to an org (Metadata API)",
		Long: "Recompose source-format metadata into a metadata-format package and deploy\n" +
			"it via the Metadata API (the reverse of sff retrieve). Select what to deploy\n" +
			"with -d (a whole source directory), -m Type:Name specifiers, or -x package.xml;\n" +
			"-m/-x resolve members from the sfdx project. Decomposed types (objects, …) are\n" +
			"re-composed and static resources are re-archived. Pass --metadata-format to deploy\n" +
			"a -d directory that is already in metadata format (with package.xml) as-is, the\n" +
			"reverse of sff retrieve --metadata-format. Use --check-only to validate without\n" +
			"saving, or --dry-run to build the package without deploying. --ignore-errors keeps\n" +
			"successfully deployed components instead of rolling back on failure, and -w/--wait\n" +
			"bounds how long to wait before returning while the deploy keeps running server-side.\n" +
			"Use --tooling for a fast deploy via the Tooling API (ApexClass/ApexTrigger/\n" +
			"ApexPage/ApexComponent and Aura/LWC bundles — which must already exist in the\n" +
			"org — plus StaticResource) — much quicker than a Metadata API round-trip for the\n" +
			"daily edit loop.",
		Example: `  sff deploy -d force-app/main/default
  sff deploy -m ApexClass:MyClass -m LWC:myCmp
  sff deploy -x manifest/package.xml --check-only
  sff deploy -d force-app -l RunSpecifiedTests --tests MyTest --tests OtherTest
  sff deploy -d ./mdapi --metadata-format
  sff deploy -m ApexClass --dry-run
  sff deploy -d force-app --ignore-errors -w 10
  sff deploy -m ApexClass:MyClass -t`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sourceDir == "" && len(metadata) == 0 && manifest == "" {
				return fmt.Errorf("specify what to deploy with -d, -m, or -x")
			}
			if metadataFormat && sourceDir == "" {
				return fmt.Errorf("--metadata-format requires -d pointing at a metadata-format directory")
			}
			if wait < 0 {
				return fmt.Errorf("--wait must be zero or positive (minutes)")
			}
			if tooling {
				if bad := incompatibleWithTooling(metadataFormat, ignoreWarnings, ignoreErrors, testLevel, tests); bad != "" {
					return fmt.Errorf("--tooling cannot be combined with %s", bad)
				}
				sel := deploySelection{sourceDir: sourceDir, metadata: metadata, manifest: manifest, projectDir: projectDir}
				return runToolingDeploy(cmd.Context(), sel, apiVersion, checkOnly, dryRun, time.Duration(wait)*time.Minute)
			}
			level, err := resolveTestLevel(testLevel, tests)
			if err != nil {
				return err
			}
			opts := mdapi.DeployOptions{
				CheckOnly:       checkOnly,
				RollbackOnError: !ignoreErrors,
				IgnoreWarnings:  ignoreWarnings,
				TestLevel:       level,
				RunTests:        tests,
			}
			sel := deploySelection{sourceDir: sourceDir, metadata: metadata, manifest: manifest, projectDir: projectDir}
			return runDeploy(cmd.Context(), sel, apiVersion, dryRun, metadataFormat, time.Duration(wait)*time.Minute, opts)
		},
	}
	cmd.Flags().StringVarP(&sourceDir, "source-dir", "d", "", "source-format directory to deploy")
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to deploy as Type or Type:Name (repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml listing what to deploy")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to resolve -m/-x members from (default: search up from cwd)")
	cmd.Flags().BoolVarP(&checkOnly, "check-only", "c", false, "validate the deploy without saving changes")
	cmd.Flags().StringVarP(&testLevel, "test-level", "l", "", "NoTestRun|RunSpecifiedTests|RunLocalTests|RunAllTestsInOrg")
	cmd.Flags().StringArrayVar(&tests, "tests", nil, "Apex test class to run (repeatable; requires -l RunSpecifiedTests)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "build the package and print the manifest without deploying")
	cmd.Flags().BoolVar(&metadataFormat, "metadata-format", false, "deploy the -d directory as-is (already metadata format with package.xml)")
	cmd.Flags().BoolVarP(&tooling, "tooling", "t", false, "fast deploy via the Tooling API (existing Apex/VF + Aura/LWC, plus static resources)")
	cmd.Flags().BoolVar(&ignoreWarnings, "ignore-warnings", false, "succeed even if the deploy reports warnings")
	cmd.Flags().BoolVar(&ignoreErrors, "ignore-errors", false, "don't roll back on failure; keep components that deployed successfully")
	cmd.Flags().IntVarP(&wait, "wait", "w", 0, "minutes to wait for the deploy to finish (0 = wait indefinitely)")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.MarkFlagsMutuallyExclusive("source-dir", "metadata", "manifest")
	addTargetOrgFlag(cmd)
	return cmd
}

// deploySelection captures the mutually exclusive ways to choose what to deploy.
type deploySelection struct {
	sourceDir  string   // -d: a whole source-format directory
	metadata   []string // -m: Type or Type:Name specifiers
	manifest   string   // -x: a package.xml path
	projectDir string   // where to resolve -m/-x members from
}

// resolveTestLevel canonicalizes the --test-level value and validates --tests usage.
func resolveTestLevel(level string, tests []string) (string, error) {
	canon := ""
	if level != "" {
		var ok bool
		canon, ok = testLevels[strings.ToLower(strings.TrimSpace(level))]
		if !ok {
			return "", fmt.Errorf("invalid --test-level %q (want NoTestRun, RunSpecifiedTests, RunLocalTests, or RunAllTestsInOrg)", level)
		}
	}
	if len(tests) > 0 && canon != mdapi.TestLevelSpecified {
		return "", fmt.Errorf("--tests requires --test-level RunSpecifiedTests")
	}
	if canon == mdapi.TestLevelSpecified && len(tests) == 0 {
		return "", fmt.Errorf("--test-level RunSpecifiedTests requires at least one --tests")
	}
	return canon, nil
}

func runDeploy(ctx context.Context, sel deploySelection, apiVersion string, dryRun, metadataFormat bool, wait time.Duration, opts mdapi.DeployOptions) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := mdapi.New(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	entries, pkg, err := buildDeployPackage(ctx, client, sel, metadataFormat)
	if err != nil {
		return err
	}

	pkgXML, ok := entries["package.xml"]
	if !ok {
		if pkgXML, err = pkg.XML(); err != nil {
			return err
		}
		entries["package.xml"] = pkgXML
	}

	if dryRun {
		fmt.Printf("would deploy %d component(s) in %d file(s):\n\n", componentCount(pkg), len(entries)-1)
		fmt.Print(string(pkgXML))
		return nil
	}

	zipBytes, err := mdapi.BuildZip(entries)
	if err != nil {
		return err
	}

	verb := "deploying"
	if opts.CheckOnly {
		verb = "validating"
	}
	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	start := time.Now()
	res, err := client.DeployAndWait(ctx, zipBytes, opts, func(attempt int, r *mdapi.DeployResult) {
		fmt.Fprintf(os.Stderr, "\r%s… %s (%d/%d components)",
			verb, r.Status, r.NumberComponentsDeployed, r.NumberComponentsTotal)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			id := ""
			if res != nil {
				id = res.ID
			}
			fmt.Fprintf(os.Stderr, "timed out after %s; deploy %s is still running in %s\n", fmtDuration(wait), id, org.Username)
			return &ExitError{Code: 1}
		}
		printDeployFailures(res)
		return &ExitError{Code: 1}
	}

	noun := "deployed"
	if opts.CheckOnly {
		noun = "validated"
	}
	fmt.Printf("%s %d component(s) to %s in %s\n", noun, res.NumberComponentsDeployed, org.Username, fmtDuration(time.Since(start)))
	if res.NumberTestsCompleted > 0 {
		fmt.Printf("ran %d test(s), %d failed\n", res.NumberTestsCompleted, res.NumberTestErrors)
	}
	return nil
}

// buildDeployPackage produces the zip entries and manifest to deploy. With
// metadataFormat the -d directory is read verbatim (its package.xml kept as-is);
// otherwise the selection is recomposed from source format, with the describe
// catalog driving type/verbatim classification (a catalog failure is non-fatal —
// recompose falls back to its built-in heuristics).
func buildDeployPackage(ctx context.Context, client *mdapi.Client, sel deploySelection, metadataFormat bool) (map[string][]byte, *mdapi.Package, error) {
	if metadataFormat {
		return mdapi.LoadMetadataDir(sel.sourceDir)
	}

	catalog, _, _ := client.DescribeMetadataCached(ctx, false)
	rec, err := recomposeSelection(sel, client.APIVersion, catalog)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range rec.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return rec.Entries, rec.Package, nil
}

// recomposeSelection turns the chosen selection into metadata-format entries and
// a manifest: a whole directory for -d, or project-resolved members for -m/-x.
func recomposeSelection(sel deploySelection, version string, catalog *mdapi.DescribeResult) (*source.RecomposeResult, error) {
	if sel.sourceDir != "" {
		return source.RecomposeDir(sel.sourceDir, version, catalog)
	}

	var pkg *mdapi.Package
	var err error
	if sel.manifest != "" {
		pkg, err = mdapi.LoadManifest(sel.manifest)
	} else {
		pkg, err = mdapi.ParseSpecifiers(sel.metadata, version)
	}
	if err != nil {
		return nil, err
	}

	start := sel.projectDir
	if start == "" {
		start = "."
	}
	proj, err := project.Find(start)
	if err != nil {
		return nil, err
	}
	return source.RecomposeMembers(proj, pkg, version, catalog)
}

// printDeployFailures writes component and test failures to stderr.
func printDeployFailures(res *mdapi.DeployResult) {
	if res == nil {
		return
	}
	if res.ErrorMessage != "" {
		fmt.Fprintln(os.Stderr, "error:", res.ErrorMessage)
	}
	failures := append([]mdapi.ComponentMessage(nil), res.ComponentFailures...)
	sort.Slice(failures, func(i, j int) bool { return failures[i].FullName < failures[j].FullName })
	for _, f := range failures {
		loc := f.FullName
		if f.ComponentType != "" {
			loc = f.ComponentType + ":" + f.FullName
		}
		if f.LineNumber > 0 {
			loc = fmt.Sprintf("%s (line %d)", loc, f.LineNumber)
		}
		fmt.Fprintf(os.Stderr, "  %s — %s\n", loc, f.Problem)
	}
	for _, t := range res.TestFailures {
		fmt.Fprintf(os.Stderr, "  test %s.%s — %s\n", t.Name, t.MethodName, t.Message)
	}
}

// toolingSupported is the set of metadata types --tooling can deploy.
var toolingSupported = map[string]bool{
	"ApexClass":                true,
	"ApexTrigger":              true,
	"ApexPage":                 true,
	"ApexComponent":            true,
	"StaticResource":           true,
	"AuraDefinitionBundle":     true,
	"LightningComponentBundle": true,
}

// incompatibleWithTooling returns a description of the first flag that cannot be
// used with --tooling, or "" if the combination is valid.
func incompatibleWithTooling(metadataFormat, ignoreWarnings, ignoreErrors bool, testLevel string, tests []string) string {
	switch {
	case metadataFormat:
		return "--metadata-format"
	case ignoreWarnings:
		return "--ignore-warnings"
	case ignoreErrors:
		return "--ignore-errors"
	case testLevel != "" || len(tests) > 0:
		return "--test-level/--tests (the Tooling API does not run tests)"
	}
	return ""
}

// runToolingDeploy resolves the selection to Apex/Visualforce bodies and pushes
// them through the Tooling API container flow.
func runToolingDeploy(ctx context.Context, sel deploySelection, apiVersion string, checkOnly, dryRun bool, wait time.Duration) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := sfapi.New(org)
	if v := strings.TrimPrefix(apiVersion, "v"); v != "" {
		client.APIVersion = "v" + v
	}

	in, skipped, err := resolveToolingComponents(sel, strings.TrimPrefix(client.APIVersion, "v"))
	if err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "skipping %s (not deployable via --tooling)\n", s)
	}
	total := len(in.Apex) + len(in.Static) + len(in.Aura) + len(in.Lwc)
	if total == 0 {
		return fmt.Errorf("no Apex/Visualforce/static-resource/Aura/LWC components to deploy via --tooling")
	}
	if checkOnly && (len(in.Static) > 0 || len(in.Aura) > 0 || len(in.Lwc) > 0) {
		return fmt.Errorf("--check-only cannot be used with static resources, Aura, or LWC (the Tooling API has no validate-only mode for them); drop --check-only or deploy them via the Metadata API")
	}

	if dryRun {
		fmt.Printf("would deploy %d component(s) via the Tooling API:\n", total)
		for _, c := range in.Apex {
			fmt.Printf("  %s:%s\n", c.Type, c.Name)
		}
		for _, s := range in.Static {
			fmt.Printf("  StaticResource:%s\n", s.Name)
		}
		for _, a := range in.Aura {
			fmt.Printf("  AuraDefinitionBundle:%s (%d file(s))\n", a.Name, len(a.Files))
		}
		for _, l := range in.Lwc {
			fmt.Printf("  LightningComponentBundle:%s (%d file(s))\n", l.Name, len(l.Files))
		}
		return nil
	}

	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	verb := "deploying"
	if checkOnly {
		verb = "validating"
	}
	start := time.Now()
	res, err := client.ToolingDeploy(ctx, in, checkOnly, func(state string) {
		fmt.Fprintf(os.Stderr, "\r%s… %s", verb, state)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "timed out after %s; the tooling deploy may still be running in %s\n", fmtDuration(wait), org.Username)
			return &ExitError{Code: 1}
		}
		printCompileErrors(res)
		return &ExitError{Code: 1}
	}

	noun := "deployed"
	if checkOnly {
		noun = "validated"
	}
	fmt.Printf("%s %d component(s) to %s in %s\n", noun, len(res.Succeeded), org.Username, fmtDuration(time.Since(start)))
	return nil
}

// resolveToolingComponents turns the selection into a ToolingDeployInput by
// recomposing it to metadata-format entries (reusing the same conversion as the
// Metadata API path, including static-resource re-archiving) and routing each
// entry to its Tooling mechanism. A -d directory contributes every supported
// component and reports the rest as skipped; -m/-x reject unsupported types and
// wildcards up front.
func resolveToolingComponents(sel deploySelection, version string) (in sfapi.ToolingDeployInput, skipped []string, err error) {
	rec, err := recomposeToolingEntries(sel, version)
	if err != nil {
		return sfapi.ToolingDeployInput{}, nil, err
	}

	staticBody := map[string][]byte{}
	staticMeta := map[string][]byte{}
	auraFiles := map[string][]sfapi.AuraFile{}
	lwcFiles := map[string][]sfapi.LwcFile{}
	skip := map[string]bool{}
	for p, data := range rec.Entries {
		folder, _, _ := strings.Cut(p, "/")
		base := path.Base(p)
		switch folder {
		case "classes":
			if strings.HasSuffix(base, ".cls") {
				in.Apex = append(in.Apex, sfapi.ToolingComponent{Type: "ApexClass", Name: strings.TrimSuffix(base, ".cls"), Body: string(data)})
			}
		case "triggers":
			if strings.HasSuffix(base, ".trigger") {
				in.Apex = append(in.Apex, sfapi.ToolingComponent{Type: "ApexTrigger", Name: strings.TrimSuffix(base, ".trigger"), Body: string(data)})
			}
		case "pages":
			if strings.HasSuffix(base, ".page") {
				in.Apex = append(in.Apex, sfapi.ToolingComponent{Type: "ApexPage", Name: strings.TrimSuffix(base, ".page"), Body: string(data)})
			}
		case "components":
			if strings.HasSuffix(base, ".component") {
				in.Apex = append(in.Apex, sfapi.ToolingComponent{Type: "ApexComponent", Name: strings.TrimSuffix(base, ".component"), Body: string(data)})
			}
		case "staticresources":
			switch {
			case strings.HasSuffix(base, ".resource"):
				staticBody[strings.TrimSuffix(base, ".resource")] = data
			case strings.HasSuffix(base, ".resource-meta.xml"):
				staticMeta[strings.TrimSuffix(base, ".resource-meta.xml")] = data
			}
		case "aura":
			segs := strings.Split(p, "/")
			if len(segs) >= 2 {
				if defType, format, ok := auraDef(base); ok {
					auraFiles[segs[1]] = append(auraFiles[segs[1]], sfapi.AuraFile{DefType: defType, Format: format, Source: string(data)})
				}
			}
		case "lwc":
			segs := strings.Split(p, "/")
			if len(segs) >= 2 {
				format := strings.TrimPrefix(path.Ext(base), ".")
				lwcFiles[segs[1]] = append(lwcFiles[segs[1]], sfapi.LwcFile{FilePath: p, Format: format, Source: string(data)})
			}
		default:
			skip[skipLabel(p)] = true
		}
	}

	for name, body := range staticBody {
		ct, cc := "application/octet-stream", "Private"
		if meta := staticMeta[name]; meta != nil {
			ct = source.MetaContentType(meta)
			cc = source.MetaCacheControl(meta)
		}
		in.Static = append(in.Static, sfapi.ToolingStaticResource{Name: name, ContentType: ct, CacheControl: cc, Body: body})
	}

	for name, files := range auraFiles {
		sort.Slice(files, func(i, j int) bool { return files[i].DefType < files[j].DefType })
		in.Aura = append(in.Aura, sfapi.ToolingAuraBundle{Name: name, Files: files})
	}

	for name, files := range lwcFiles {
		sort.Slice(files, func(i, j int) bool { return files[i].FilePath < files[j].FilePath })
		in.Lwc = append(in.Lwc, sfapi.ToolingLwcBundle{Name: name, Files: files})
	}

	sort.Slice(in.Apex, func(i, j int) bool { return in.Apex[i].Name < in.Apex[j].Name })
	sort.Slice(in.Static, func(i, j int) bool { return in.Static[i].Name < in.Static[j].Name })
	sort.Slice(in.Aura, func(i, j int) bool { return in.Aura[i].Name < in.Aura[j].Name })
	sort.Slice(in.Lwc, func(i, j int) bool { return in.Lwc[i].Name < in.Lwc[j].Name })
	for s := range skip {
		skipped = append(skipped, s)
	}
	sort.Strings(skipped)
	return in, skipped, nil
}

// recomposeToolingEntries recomposes the selection to metadata-format entries
// with no describe catalog (heuristics only, so resolution stays offline). For
// -m/-x it first rejects unsupported types and wildcards.
func recomposeToolingEntries(sel deploySelection, version string) (*source.RecomposeResult, error) {
	if sel.sourceDir != "" {
		return source.RecomposeDir(sel.sourceDir, version, nil)
	}

	var pkg *mdapi.Package
	var err error
	if sel.manifest != "" {
		pkg, err = mdapi.LoadManifest(sel.manifest)
	} else {
		pkg, err = mdapi.ParseSpecifiers(sel.metadata, version)
	}
	if err != nil {
		return nil, err
	}
	for _, t := range pkg.Types {
		if !toolingSupported[t.Name] {
			return nil, fmt.Errorf("--tooling does not support %s (only ApexClass, ApexTrigger, ApexPage, ApexComponent, StaticResource)", t.Name)
		}
		for _, m := range t.Members {
			if m == "*" {
				return nil, fmt.Errorf("--tooling needs explicit names; %s wildcard is not supported", t.Name)
			}
		}
	}

	start := sel.projectDir
	if start == "" {
		start = "."
	}
	proj, err := project.Find(start)
	if err != nil {
		return nil, err
	}
	return source.RecomposeMembers(proj, pkg, version, nil)
}

// auraDef maps an Aura bundle file name to its AuraDefinition DefType and Format,
// or ok=false for files that aren't Aura definitions (e.g. a bundle -meta.xml).
func auraDef(file string) (defType, format string, ok bool) {
	if strings.HasSuffix(file, "-meta.xml") {
		return "", "", false
	}
	switch {
	case strings.HasSuffix(file, "Controller.js"):
		return "CONTROLLER", "JS", true
	case strings.HasSuffix(file, "Helper.js"):
		return "HELPER", "JS", true
	case strings.HasSuffix(file, "Renderer.js"):
		return "RENDERER", "JS", true
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".cmp":
		return "COMPONENT", "XML", true
	case ".app":
		return "APPLICATION", "XML", true
	case ".evt":
		return "EVENT", "XML", true
	case ".intf":
		return "INTERFACE", "XML", true
	case ".tokens":
		return "TOKENS", "XML", true
	case ".design":
		return "DESIGN", "XML", true
	case ".auradoc":
		return "DOCUMENTATION", "XML", true
	case ".svg":
		return "SVG", "SVG", true
	case ".css":
		return "STYLE", "CSS", true
	}
	return "", "", false
}

// skipLabel summarizes an unsupported entry path as "folder/name" for reporting.
func skipLabel(p string) string {
	segs := strings.Split(p, "/")
	if len(segs) >= 2 {
		return segs[0] + "/" + segs[1]
	}
	return segs[0]
}

// printCompileErrors writes Tooling deploy compiler failures to stderr.
func printCompileErrors(res *sfapi.ToolingDeployResult) {
	if res == nil {
		return
	}
	errs := append([]sfapi.CompileError(nil), res.Errors...)
	sort.Slice(errs, func(i, j int) bool { return errs[i].Component < errs[j].Component })
	for _, e := range errs {
		loc := e.Component
		if e.Line > 0 {
			loc = fmt.Sprintf("%s (line %d)", loc, e.Line)
		}
		fmt.Fprintf(os.Stderr, "  %s — %s\n", loc, e.Problem)
	}
}

// componentCount totals the members across a manifest's types.
func componentCount(pkg *mdapi.Package) int {
	n := 0
	for _, t := range pkg.Types {
		n += len(t.Members)
	}
	return n
}
