// Minimal VS Code shim: launches `seamark lsp` for the first workspace
// folder and lets vscode-languageclient do the rest. VS Code has no
// generic LSP client configuration, so this thin extension is the
// supported way to attach a secondary server.
const vscode = require('vscode');
const { LanguageClient } = require('vscode-languageclient/node');

let client;

function activate(context) {
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (!folder) {
    return;
  }

  const bin = vscode.workspace.getConfiguration('seamark').get('path', 'seamark');

  client = new LanguageClient(
    'seamark',
    'Seamark',
    { command: bin, args: ['lsp', '-C', folder.uri.fsPath] },
    {
      documentSelector: [
        { scheme: 'file', language: 'go' },
        { scheme: 'file', language: 'python' },
        { scheme: 'file', language: 'typescript' },
        { scheme: 'file', language: 'typescriptreact' },
        { scheme: 'file', language: 'javascript' },
        { scheme: 'file', language: 'javascriptreact' },
      ],
    }
  );

  context.subscriptions.push({ dispose: () => client?.stop() });
  client.start();
}

function deactivate() {
  return client?.stop();
}

module.exports = { activate, deactivate };
