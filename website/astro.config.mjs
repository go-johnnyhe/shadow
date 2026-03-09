import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://go-johnnyhe.github.io',
  base: '/shadow',
  output: 'static',
  vite: {
    server: {
      allowedHosts: ['myapp.test'],
    },
  },
});
