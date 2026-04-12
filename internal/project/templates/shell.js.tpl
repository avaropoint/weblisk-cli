// Shell Island — bootstraps Weblisk client-side modules.
// Loaded on every page via <script type="module">.

import { hydrateIslands } from 'weblisk/core/hydrate.js';

// Hydrate any elements with data-island attributes
hydrateIslands();
