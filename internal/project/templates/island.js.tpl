import { enhance } from 'weblisk';

enhance('[data-island="{{.Name}}"]', (el, { $, $$, effect }) => {
  // {{.Name}} island — add interactive behavior here
});
