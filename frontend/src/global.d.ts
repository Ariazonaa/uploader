// Side-effect CSS imports (main.tsx imports index.css). TypeScript 7 checks
// side-effect imports, and the project deliberately does without the
// vite/client ambient types, so declare just this one module shape.
declare module "*.css";
