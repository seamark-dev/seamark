-- Seamark: history/effects layer as a secondary LSP server.
-- Attaches alongside gopls/pyright/tsserver; APIs are nvim 0.9-compatible.
-- Binary: `make install` in the seamark repo (~/.local/bin/seamark).

local group = vim.api.nvim_create_augroup("seamark", { clear = true })

local function find_root(buf)
  if vim.fs.root then -- nvim 0.10+
    return vim.fs.root(buf, ".git")
  end

  local dir = vim.fs.dirname(vim.api.nvim_buf_get_name(buf))
  local hit = vim.fs.find(".git", { upward = true, path = dir })[1]
  return hit and vim.fs.dirname(hit) or nil
end

vim.api.nvim_create_autocmd("FileType", {
  group = group,
  pattern = { "go", "python", "typescript", "typescriptreact", "javascript", "javascriptreact" },
  callback = function(args)
    local root = find_root(args.buf)
    if not root then
      return
    end

    vim.lsp.start({
      name = "seamark",
      cmd = { vim.fn.expand("~/.local/bin/seamark"), "lsp", "-C", root },
      root_dir = root,
    })
  end,
})

-- Code lenses need explicit refresh.
vim.api.nvim_create_autocmd({ "BufEnter", "BufWritePost", "CursorHold" }, {
  group = group,
  callback = function()
    pcall(vim.lsp.codelens.refresh)
  end,
})

-- <leader>K: ask seamark directly for its hover. 0.9 does not merge
-- multi-server hovers on K, so this is the reliable way to see seamark's
-- answer next to the primary server's docs.
local function seamark_hover()
  local get_clients = vim.lsp.get_clients or vim.lsp.get_active_clients
  local clients = get_clients({ name = "seamark", bufnr = 0 })

  if #clients == 0 then
    vim.notify("seamark: not attached to this buffer", vim.log.levels.WARN)
    return
  end

  local params = vim.lsp.util.make_position_params(0, "utf-16")

  -- buf_request has a stable signature on 0.8-0.11; nvim 0.11 removed the
  -- legacy client.request(...) dot-call form. Every hover-capable client
  -- gets the request; only seamark's answer is displayed.
  vim.lsp.buf_request(0, "textDocument/hover", params, function(err, result, ctx)
    local from = vim.lsp.get_client_by_id(ctx.client_id)
    if not from or from.name ~= "seamark" then
      return
    end

    if err then
      vim.notify("seamark: " .. tostring(err.message or err), vim.log.levels.ERROR)
      return
    end

    if not (result and result.contents and result.contents.value and result.contents.value ~= "") then
      vim.notify("seamark: nothing indexed here", vim.log.levels.INFO)
      return
    end

    local lines = vim.split(result.contents.value, "\n")
    vim.lsp.util.open_floating_preview(lines, "markdown", { border = "rounded", focusable = true })
  end)
end

vim.keymap.set("n", "<leader>K", seamark_hover, { desc = "Seamark: why (hover)" })
