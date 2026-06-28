import { Navbar } from "./components/navbar";
import { Hero } from "./components/hero";
import { HowItWorks } from "./components/how-it-works";
import { Bento } from "./components/bento";
import { Architecture } from "./components/architecture";
import { Compare } from "./components/compare";
import { CTA } from "./components/cta";
import { Footer } from "./components/footer";

export default function Home() {
  return (
    <main>
      <Navbar />
      <Hero />
      <HowItWorks />
      <Bento />
      <Architecture />
      <Compare />
      <CTA />
      <Footer />
    </main>
  );
}
