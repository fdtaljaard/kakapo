import { Fragment } from "react"
import { Link } from "react-router-dom"

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb"
import { Skeleton } from "@/components/ui/skeleton"

export interface Crumb {
  label: string | null // null while it loads
  to?: string // the last crumb is the current page and has none
}

// `Instances › {instance} › {database} › {card}` on every page below Home.
export function PageCrumbs({ crumbs }: { crumbs: Crumb[] }) {
  return (
    <Breadcrumb>
      <BreadcrumbList>
        <BreadcrumbItem>
          <BreadcrumbLink asChild>
            <Link to="/">Instances</Link>
          </BreadcrumbLink>
        </BreadcrumbItem>
        {crumbs.map((c, i) => (
          <Fragment key={i}>
            <BreadcrumbSeparator />
            <BreadcrumbItem className="min-w-0">
              {c.label === null ? (
                <Skeleton className="h-4 w-24" />
              ) : c.to ? (
                <BreadcrumbLink asChild>
                  <Link to={c.to} className="break-all">
                    {c.label}
                  </Link>
                </BreadcrumbLink>
              ) : (
                <BreadcrumbPage className="break-all">{c.label}</BreadcrumbPage>
              )}
            </BreadcrumbItem>
          </Fragment>
        ))}
      </BreadcrumbList>
    </Breadcrumb>
  )
}
