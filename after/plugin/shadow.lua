-- ~/.config/nvim/after/plugin/shadow.lua
vim.opt.autoread = true
vim.opt.updatetime = 500
vim.opt.swapfile = false
local group = vim.api.nvim_create_augroup("shadow_autoread", { clear = true })


vim.api.nvim_create_autocmd(
  { "FocusGained", "BufEnter", "CursorHold", "CursorHoldI", "TermEnter" },
  {
    group = group,
    pattern = "*",
    callback = function()
      -- pcall avoids 'checktime' errors in special buffers
      pcall(vim.cmd, "checktime")
    end,
    desc = "Reload buffer if the file changed on disk",
  }
)