import type { ComponentType } from 'react';
import { MusicPanel } from './MusicPanel';
import { PhotographyPanel } from './PhotographyPanel';

// About Panels: self-contained personal-interest sections rendered in the about
// area (see CONTEXT.md "About Panel"). Each panel owns its own data + layout.
// Add a new panel by appending it here — the page renders the registry in order.
export const aboutPanels: ComponentType[] = [MusicPanel, PhotographyPanel];
