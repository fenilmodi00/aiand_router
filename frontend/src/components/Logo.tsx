import Link from "next/link";
import logo from "../../public/aiand.svg";

interface LogoProps {
  href?: string;
}

// Static import so Next emits a content-hashed URL. A fixed /ui/aiand.svg
// URL survives rebrands behind browser and intermediary caches, which showed
// the pre-restyle logo after deploy. Next includes the /ui basePath.
const LOGO_SRC = logo.src;

export function Logo({ href = "/" }: LogoProps) {
  return (
    <Link href={href} className="inline-flex shrink-0" aria-label="Aiand Router">
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src={LOGO_SRC} alt="Aiand Router" width={72} height={24} className="h-6 w-auto shrink-0" />
    </Link>
  );
}
