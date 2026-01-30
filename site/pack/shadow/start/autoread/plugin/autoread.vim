if exists('g:loaded_mycli_autoread') | finish | endif
let g:loaded_mycli_autoread = 1

set autoread
set updatetime=500

augroup mycli_autoread
    autocmd!
    autocmd FocusGained,BufEnter,CursorHold,CursorHoldI * checktime
augroup END
