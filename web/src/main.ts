import { mount } from 'svelte';
import { registerServiceWorker } from './lib/pwa/register';
import App from './App.svelte';
import './styles/fonts.css';
import './styles/tokens.css';
import './styles/base.css';

const target = document.getElementById('app');
if (!target) {
  throw new Error('bubbles: missing #app mount point');
}

mount(App, { target });
void registerServiceWorker();
