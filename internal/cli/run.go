package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aimount/aimount-go/internal/consoleapi"
)

const defaultAPIURL = "https://api.aimount.dev"

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage(stdout)
		return 0
	}

	global := flag.NewFlagSet("aimount", flag.ContinueOnError)
	global.SetOutput(stderr)
	orgID := global.String("org", "", "organization ID")
	if err := global.Parse(args); err != nil {
		return 2
	}
	args = global.Args()
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	for _, arg := range args {
		if arg == "help" || arg == "--help" || arg == "-h" {
			usage(stdout)
			return 0
		}
	}

	if len(args) == 2 && args[0] == "auth" && args[1] == "whoami" {
		client, code := newClient(stderr)
		if code != 0 {
			return code
		}
		principal, err := client.GetPrincipal(ctx)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "owner\t%s\t%s\n", principal.OwnerType, principal.OwnerID)
		for _, id := range principal.OrgIDs {
			fmt.Fprintf(stdout, "org\t%s\n", id)
		}
		return 0
	}

	if args[0] != "agents" {
		usage(stderr)
		return 2
	}
	if len(args) == 2 && args[1] == "list" {
		client, resolvedOrg, code := clientForOrg(ctx, *orgID, stderr)
		if code != 0 {
			return code
		}
		agents, err := client.ListAgents(ctx, resolvedOrg)
		if err != nil {
			return fail(stderr, err)
		}
		for _, agent := range agents {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", agent.Slug, agent.Name, agent.Status)
		}
		return 0
	}
	if len(args) >= 3 && args[1] == "set" {
		return setAgent(ctx, *orgID, args[2:], stdout, stderr)
	}
	if len(args) >= 5 && args[1] == "instruction" && args[2] == "set" {
		return setInstruction(ctx, *orgID, args[3:], stdout, stderr)
	}
	usage(stderr)
	return 2
}

func setAgent(ctx context.Context, orgID string, args []string, stdout, stderr io.Writer) int {
	slug := strings.TrimSpace(args[0])
	flags := flag.NewFlagSet("agents set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "agent name")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || slug == "" || strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, "usage: aimount [--org ID] agents set <slug> --name <name>")
		return 2
	}
	client, resolvedOrg, code := clientForOrg(ctx, orgID, stderr)
	if code != 0 {
		return code
	}
	agent, err := client.UpsertAgent(ctx, resolvedOrg, slug, strings.TrimSpace(*name))
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "set agent\t%s\t%s\n", agent.Slug, agent.Name)
	return 0
}

func setInstruction(ctx context.Context, orgID string, args []string, stdout, stderr io.Writer) int {
	slug := strings.TrimSpace(args[0])
	flags := flag.NewFlagSet("agents instruction set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("file", "", "instruction file")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || slug == "" || strings.TrimSpace(*path) == "" {
		fmt.Fprintln(stderr, "usage: aimount [--org ID] agents instruction set <slug> --file <path>")
		return 2
	}
	content, err := os.ReadFile(*path)
	if err != nil {
		return fail(stderr, fmt.Errorf("read instruction file: %w", err))
	}
	instruction := strings.TrimSpace(string(content))
	if instruction == "" {
		return fail(stderr, errors.New("instruction file is empty"))
	}
	client, resolvedOrg, code := clientForOrg(ctx, orgID, stderr)
	if code != 0 {
		return code
	}
	version, err := client.CreateAgentInstructionVersion(ctx, resolvedOrg, slug, instruction)
	if err != nil {
		return fail(stderr, err)
	}
	if err := client.ReleaseAgentInstructionVersion(ctx, resolvedOrg, slug, version.ID); err != nil {
		fmt.Fprintf(stderr, "error: created instruction version %s but release failed: %v\n", version.ID, err)
		return 1
	}
	fmt.Fprintf(stdout, "released instruction\t%s\t%s\n", slug, version.ID)
	return 0
}

func resolveOrg(ctx context.Context, client *consoleapi.Client, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	principal, err := client.GetPrincipal(ctx)
	if err != nil {
		return "", err
	}
	switch len(principal.OrgIDs) {
	case 0:
		return "", errors.New("no accessible organizations")
	case 1:
		return principal.OrgIDs[0], nil
	default:
		return "", errors.New("multiple organizations are accessible; provide --org")
	}
}

func newClient(stderr io.Writer) (*consoleapi.Client, int) {
	token := strings.TrimSpace(os.Getenv("AIMOUNT_TOKEN"))
	if token == "" {
		fmt.Fprintln(stderr, "error: AIMOUNT_TOKEN is required")
		return nil, 1
	}
	baseURL := strings.TrimSpace(os.Getenv("AIMOUNT_API_URL"))
	if baseURL == "" {
		baseURL = defaultAPIURL
	}
	client, err := consoleapi.New(consoleapi.Config{BaseURL: baseURL, Token: token})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 1
	}
	return client, 0
}

func clientForOrg(ctx context.Context, explicit string, stderr io.Writer) (*consoleapi.Client, string, int) {
	client, code := newClient(stderr)
	if code != 0 {
		return nil, "", code
	}
	orgID, err := resolveOrg(ctx, client, explicit)
	if err != nil {
		return nil, "", fail(stderr, err)
	}
	return client, orgID, 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "error:", err)
	return 1
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  aimount auth whoami
  aimount agents list
  aimount agents set <slug> --name <name>
  aimount agents instruction set <slug> --file <path>

Global option: --org ID`)
}
