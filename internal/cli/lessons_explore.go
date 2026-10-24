package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds lessons --explore --hook (contract L2). The command
// connects the parts: the client adapter decodes the read event and
// encodes the reply, and delivery.Explore owns selection, dedupe,
// emission order, and the firing record. The command runs no check,
// blocks nothing, and never touches the edit hook state.

// exploreConflicts lists the lessons modes and decision flags that
// --explore refuses, in the order of the help text. The first flag that
// the call sets names the conflict (contracts §4.2). --hook is not here,
// because --explore needs it. --json is not here, because verifyFlagError
// refuses it first. Each modifier flag, for example --client, keeps its
// own guard and is not here.
//
// Skills grant the prefixes `seamark lessons --file *` and `seamark
// lessons --region *` without a prompt (plan step P3.12). Without this
// list, such a call with --explore --hook reaches the exploration hook.
// That hook writes the exploration state and a firing record, so a
// skill turn could hide advice from later reads (triage item P3.12-2).
var exploreConflicts = []string{
	"file", "list", "region", "stats", "distill", "proposals", "verify", "prepare", "set-mode",
	"apply", "dismiss", "prune", "retarget", "extract-triggers", "hook-reset",
}

// exploreFlagError checks the flags of an --explore call before any
// dispatch. --explore combines with no other mode and needs --hook. The
// mode conflict comes first, so a call that also lacks --hook names the
// flag that it must drop. The plain --hook and --hook-reset calls keep
// their guards unchanged (contracts §4.1).
func exploreFlagError(cmd *cobra.Command, explore, hookMode bool) error {
	if !explore {
		return nil
	}

	for _, name := range exploreConflicts {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--explore does not combine with --%s", name)
		}
	}

	// --explore names the event that --hook reads. Without --hook
	// nothing reads the event, so the call is a usage error.
	if !hookMode {
		return errors.New("--explore needs --hook: it reads one native read event from stdin")
	}

	return nil
}

// runLessonsExplore implements the exploration hook. It must never fail
// the read that it follows: every decode, path, index, config, state,
// or log failure gives exit 0, and most give no output. Only a failed
// reply write returns an error, because then the hook itself failed.
//
// A client without a structured read adapter writes nothing. Codex has
// none, so `--explore --hook --client codex` exits 0 without a write.
func runLessonsExplore(cmd *cobra.Command, opts *options, clientID string) error {
	client, ok := lessonsHookClient(cmd, clientID)
	if !ok || client.Explores == nil {
		return nil
	}

	payload, err := integration.ReadHookPayload(cmd.InOrStdin())
	if err != nil {
		return nil // nothing to say; never block the agent
	}

	event, err := client.Explores.DecodeExplore(payload)
	if err != nil || len(event.Paths) == 0 {
		return nil
	}

	// The edit hook opens the index the same way, so a read and an edit
	// of one file select from one index and key one root.
	st, root, err := openHookIndex(opts)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()

	if event.CWD == "" {
		// A client that sends no working directory sends paths relative
		// to the hook process, as for the edit hook.
		event.CWD, _ = os.Getwd()
	}

	emit := func(advice string) error {
		reply, err := client.Explores.EncodeExploreAdvice(advice, event.Read)
		if err != nil {
			return err
		}

		_, err = cmd.OutOrStdout().Write(reply.Stdout)

		return err
	}

	// The hook keeps the read error, so the firing log can name the
	// guidance invalid. The advice still uses the defaults then.
	cfg, cfgErr := reviews.LoadConfig(root)
	if cfgErr != nil {
		cfg = reviews.DefaultConfig()
	}

	// One identity root and one read of the recording switch per run. The
	// switch gates the evaluation records only, never the advice.
	idRoot := hookIdentityRoot(opts)

	_, err = delivery.Explore(cmd.Context(), st, delivery.ExploreRequest{
		Root: root, IdentityRoot: idRoot, ClientID: client.ID, Mechanism: client.Explores.ExploreMechanism(),
		Event: event, Config: cfg, ConfigErr: cfgErr,
		RecordEvaluations: recordEvaluations(),
	}, emit)

	// Only a failed reply is the hook's own failure. A failed lookup
	// stays quiet: the advice is optional and the read already ran.
	var emitErr *delivery.EmitError
	if errors.As(err, &emitErr) {
		return emitErr.Err
	}

	return nil
}
