/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_RELEASE_REPOSITORY?: string;
  readonly VITE_RELEASE_REF?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
