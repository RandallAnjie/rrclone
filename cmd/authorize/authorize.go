// Package authorize provides the authorize command.
package authorize

import (
	"context"
	"errors"
	"fmt"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/flags"
	"github.com/rclone/rclone/lib/accountrotation"
	"github.com/spf13/cobra"
)

var (
	noAutoBrowser bool
	template      string
	accountFile   string
)

func init() {
	cmd.Root.AddCommand(commandDefinition)
	cmdFlags := commandDefinition.Flags()
	flags.BoolVarP(cmdFlags, &noAutoBrowser, "auth-no-open-browser", "", false, "Do not automatically open auth link in default browser", "")
	flags.StringVarP(cmdFlags, &template, "template", "", "", "The path to a custom Go template for generating HTML responses", "")
	flags.StringVarP(cmdFlags, &accountFile, "account-file", "", "", "Write the token to this OAuth account file for account rotation instead of printing it", "")
}

var commandDefinition = &cobra.Command{
	Use:   "authorize <backendname> [base64_json_blob | client_id client_secret]",
	Short: `Remote authorization.`,
	Long: `Remote authorization. Used to authorize a remote or headless
rclone from a machine with a browser. Use as instructed by rclone config.
See also the [remote setup documentation](/remote_setup).

The command requires 1-3 arguments:

- Name of a backend (e.g. "drive", "s3")
- Either a base64 encoded JSON blob obtained from a previous rclone config session
- Or a client_id and client_secret pair obtained from the remote service

Use --auth-no-open-browser to prevent rclone to open auth
link in default browser automatically.

Use --template to generate HTML output via a custom Go template. If a blank
string is provided as an argument to this flag, the default template is used.

Use --account-file to write the token, with the client_id and
client_secret given, to a file for the oauth_account_files rotation of
the drive and dropbox backends, rather than printing it. Run it once
per account or app, each with its own file. An existing file is
replaced, which is how to renew an account whose token stopped working.

    rclone authorize dropbox APP_KEY APP_SECRET --account-file accounts/app1.json`,
	Annotations: map[string]string{
		"versionIntroduced": "v1.27",
	},
	RunE: func(command *cobra.Command, args []string) error {
		cmd.CheckArgs(1, 3, command, args)
		if accountFile == "" {
			return config.Authorize(context.Background(), args, noAutoBrowser, template)
		}
		return authorizeAccountFile(context.Background(), args)
	},
}

// authorizeAccountFile authorizes and writes the result to accountFile
func authorizeAccountFile(ctx context.Context, args []string) error {
	if len(args) == 2 {
		return errors.New("--account-file needs a client_id and client_secret, not a config blob")
	}
	token, err := config.AuthorizeToken(ctx, args, noAutoBrowser, template)
	if err != nil {
		return err
	}
	var clientID, clientSecret string
	if len(args) == 3 {
		clientID, clientSecret = args[1], args[2]
	}
	if err := accountrotation.WriteFile(accountFile, clientID, clientSecret, token); err != nil {
		return fmt.Errorf("couldn't write account file: %w", err)
	}
	fmt.Printf("Wrote OAuth account file %q\n", accountFile)
	return nil
}
